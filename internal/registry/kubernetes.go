package registry

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	v1Types "k8s.io/api/core/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

type kubernetesRegistry struct {
	core               v1.CoreV1Interface
	namespaceInterface v1.NamespaceInterface
	client             http.Client
	namePortStatus     *sync.Map
}

func NewKubernetesRegistry(client *kubernetes.Clientset) Registry {
	core := client.CoreV1()
	return kubernetesRegistry{
		core:               core,
		namespaceInterface: core.Namespaces(),
		client: http.Client{
			Timeout: time.Millisecond * 100,
		},
		namePortStatus: &sync.Map{},
	}
}

func (v kubernetesRegistry) ListNamespaces(ctx context.Context) ([]string, error) {
	r, err := v.namespaceInterface.List(ctx, metaV1.ListOptions{})
	if err != nil {
		return nil, err
	}

	namespaces := make([]string, len(r.Items))
	for i, item := range r.Items {
		namespaces[i] = item.Name
	}
	return namespaces, nil
}

func (v kubernetesRegistry) ListHosts(ctx context.Context, namespace string) ([]Host, error) {
	r, err := v.core.Pods(namespace).List(ctx, metaV1.ListOptions{})
	if err != nil {
		return nil, err
	}

	now := time.Now()
	hosts := make([]Host, 0, len(r.Items))
	for _, item := range r.Items {
		if item.Status.Phase != v1Types.PodRunning {
			continue
		}
		for _, container := range item.Spec.Containers {
			for _, port := range container.Ports {
				if port.Protocol != v1Types.ProtocolTCP {
					continue
				}

				portString := strconv.FormatInt(int64(port.ContainerPort), 10)
				name := item.Name + ":" + portString
				address := item.Status.PodIP + ":" + portString
				var isValid bool
				if status, exist := v.namePortStatus.Load(name); exist {
					isValid = status.(bool)
				} else {
					isValid = v.isValidAddress("http://" + address + "/debug/pprof/")
					v.namePortStatus.Store(name, isValid)
				}
				if !isValid {
					continue
				}

				hosts = append(hosts, Host{
					Name:    name,
					Address: address,
					Age:     now.Sub(item.Status.StartTime.Time),
				})
			}
		}

	}
	return hosts, nil
}

func (v kubernetesRegistry) isValidAddress(url string) bool {
	r, err := v.client.Get(url)
	if err != nil {
		fmt.Printf("check address %s fail, %s\n", url, err)
		return false
	}
	defer r.Body.Close()
	return r.StatusCode < 300
}
