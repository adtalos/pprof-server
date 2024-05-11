package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/adtalos/lib-go/coalesce"
	"github.com/adtalos/lib-go/must"
	"github.com/adtalos/pprof-server/internal/pprof"
	"github.com/adtalos/pprof-server/internal/registry"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/html/v2"
	"github.com/robfig/cron/v3"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func main() {
	isDebug := flag.Bool("debug", false, "toggle debug")
	port := flag.Int64("port", 6061, "listen port")
	vip := flag.String("vips", "", "sort hosts, split by comma")
	crontab := flag.String("cron", "", "")
	rawCronPrefixes := flag.String("keywords", "", "cron prefixes, {namespace}:{prefix}, split by comma")
	dest := flag.String("dest", "/pprof", "cron dest")
	flag.Parse()

	var config *rest.Config
	if *isDebug {
		config = must.Get(clientcmd.BuildConfigFromFlags("", filepath.Join(homedir.HomeDir(), ".kube", "config")))
	} else {
		config = must.Get(rest.InClusterConfig())
	}
	kubernetesRegistry := registry.NewKubernetesRegistry(must.Get(kubernetes.NewForConfig(config)))

	httpClient := &http.Client{
		Timeout: time.Minute,
	}
	proxyManager := pprof.NewManager(*port + 1)

	if *crontab != "" && *rawCronPrefixes != "" {
		cronjob := cron.New()
		cronPrefixes := make([][]string, 0)
		for _, cronPrefix := range strings.Split(*rawCronPrefixes, ",") {
			namespaceAndPrefix := strings.Split(cronPrefix, ":")
			if len(namespaceAndPrefix) != 2 {
				panic(fmt.Errorf("wrong cron prefix config, %s", *rawCronPrefixes))
			}
			cronPrefixes = append(cronPrefixes, namespaceAndPrefix)
		}
		cronjob.AddFunc(*crontab, func() {
			for _, cronPrefix := range cronPrefixes {
				namespace := cronPrefix[0]
				prefix := cronPrefix[1]
				hosts, err := kubernetesRegistry.ListHosts(context.Background(), namespace)
				if err != nil {
					fmt.Printf("list hosts fail, namespace %s, prefix %s, err %s\n", namespace, prefix, err)
				}
				var matchHost *registry.Host
				for _, host := range hosts {
					if !strings.HasPrefix(host.Name, prefix) {
						continue
					}
					if matchHost == nil {
						matchHost = &host
					} else if host.Age > matchHost.Age {
						matchHost = &host
					}
				}
				if matchHost == nil {
					continue
				}

				for _, t := range []string{"allocs", "block", "cmdline", "goroutine", "heap", "mutex", "profile", "threadcreate", "trace"} {
					source := matchHost.Address + "/debug/pprof/" + t + "?seconds=5"
					dir := filepath.Join(*dest, t)
					if err := os.MkdirAll(dir, os.ModePerm); err != nil && !os.IsExist(err) {
						panic(err)
					}
					if err := proxyManager.Persistent(source, filepath.Join(dir, matchHost.Name)); err != nil {
						fmt.Printf("persistent fail, namespace %s, prefix %s, name %s, err %s\n", namespace, prefix, matchHost.Name, err)
					}
				}
			}

			// cleanup
			files := must.Get(filepath.Glob(filepath.Join(*dest, "*/*.pb.gz")))
			ago := time.Now().Add(-time.Hour * 24)
			for _, file := range files {
				if info, err := os.Stat(file); err != nil {
					fmt.Printf("stat file %s fail, err %s\n", file, err)
				} else if info.ModTime().Before(ago) {
					if err := os.Remove(file); err != nil {
						fmt.Printf("delete file %s fail, err %s\n", file, err)
					}
				}
			}
		})
		cronjob.Start()
	}

	engine := html.NewFileSystem(http.Dir("./views"), ".html")
	if *isDebug {
		engine.Reload(true)
	}
	app := fiber.New(fiber.Config{
		Views: engine,
	})

	var vips []string
	if *vip != "" {
		vips = strings.Split(*vip, ",")
	}

	listNamespaces := func(ctx context.Context) ([]string, error) {
		namespaces, err := kubernetesRegistry.ListNamespaces(ctx)
		if err != nil {
			return nil, err
		}
		if len(vips) > 0 {
			sort.Slice(namespaces, func(i, j int) bool {
				return slices.Index(vips, namespaces[i]) > slices.Index(vips, namespaces[j])
			})
		}
		return namespaces, nil
	}

	type file struct {
		Name string
		Type string
	}
	app.Get("/files", func(c *fiber.Ctx) error {
		namespaces, err := listNamespaces(c.Context())
		if err != nil {
			return err
		}
		files, err := filepath.Glob(filepath.Join(*dest, "*/*.pb.gz"))
		if err != nil {
			return err
		}
		sort.Slice(files, func(i, j int) bool {
			return must.Get(os.Stat(files[i])).ModTime().After(must.Get(os.Stat(files[j])).ModTime())
		})
		sources := make([]file, len(files))
		for i, f := range files {
			parts := strings.Split(f, "/")
			sources[i] = file{
				Name: parts[len(parts)-1],
				Type: parts[len(parts)-2],
			}
		}
		return c.Render("index", fiber.Map{
			"namespace":  "files",
			"namespaces": namespaces,
			"files":      sources,
		})
	})

	app.Get("/:namespace?", func(c *fiber.Ctx) error {
		namespaces, err := listNamespaces(c.Context())
		if err != nil {
			return err
		}
		namespace := coalesce.Value(c.Params("namespace"), namespaces[0])
		hosts, err := kubernetesRegistry.ListHosts(c.Context(), namespace)
		if err != nil {
			return err
		}
		return c.Render("index", fiber.Map{
			"namespace":  namespace,
			"namespaces": namespaces,
			"hosts":      hosts,
		})
	})

	app.Get("/proxy/:address/", func(c *fiber.Ctx) error {
		address := c.Params("address")
		r, err := http.Get("http://" + address + "/debug/pprof")
		if err != nil {
			return err
		}

		c.Set("Content-type", fiber.MIMETextHTMLCharsetUTF8)
		return c.SendStream(r.Body)
	})

	app.Get("/proxy/:source/:type", func(c *fiber.Ctx) error {
		timeout, err := strconv.ParseInt(c.Query("timeout", strconv.FormatInt(int64((time.Minute*15).Seconds()), 10)), 10, 64)
		if err != nil {
			return err
		}

		source := c.Params("source")
		t := c.Params("type")
		if strings.HasSuffix(source, ".pb.gz") {
			source = filepath.Join(*dest, t, source)
		} else {
			source = source + "/debug/pprof/" + t + "?seconds=" + c.Query("seconds", "5")
		}
		proxyPort, err := proxyManager.Proxy(time.Duration(timeout)*time.Second, source)
		if err != nil {
			return err
		}

		proxyPortStr := strconv.FormatInt(proxyPort, 10)
		for {
			_, err := httpClient.Get("http://localhost:" + proxyPortStr)
			if err != nil {
				if errors.Is(err, syscall.ECONNREFUSED) {
					continue
				}
				return err
			}

			return c.Redirect("/ports/" + proxyPortStr + "/")
		}
	})

	app.Get("/ports/:port/*", func(c *fiber.Ctx) error {
		proxyPort := c.Params("port")
		r, err := httpClient.Get("http://localhost:" + proxyPort + "/ui/" + c.Params("*") + "?" + c.Request().URI().QueryArgs().String())
		if err != nil {
			return err
		}

		c.Set("Content-type", fiber.MIMETextHTMLCharsetUTF8)
		return c.SendStream(r.Body)
	})

	app.Listen(":" + strconv.FormatInt(*port, 10))
}
