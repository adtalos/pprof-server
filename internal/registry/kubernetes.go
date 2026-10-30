package registry

import (
	"context"
	"net"
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
	discovery          *portDiscovery
}

func NewKubernetesRegistry(client *kubernetes.Clientset, discoveryConcurrency int) Registry {
	core := client.CoreV1()
	return kubernetesRegistry{
		core:               core,
		namespaceInterface: core.Namespaces(),
		discovery:          newPortDiscovery(discoveryConcurrency),
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

	type candidate struct {
		key  probeKey
		host Host
	}
	candidates := make([][]candidate, 0, len(r.Items))
	active := make(map[probeKey]struct{})
	for _, item := range r.Items {
		if item.Status.Phase != v1Types.PodRunning || item.Status.PodIP == "" {
			continue
		}
		age := time.Duration(0)
		if item.Status.StartTime != nil {
			age = time.Since(item.Status.StartTime.Time)
		}
		podCandidates := make([]candidate, 0)
		for _, port := range candidatePorts(item.Spec.Containers) {
			portString := strconv.FormatInt(int64(port), 10)
			key := probeKey{namespace: item.Namespace, uid: string(item.UID), ip: item.Status.PodIP, port: port}
			active[key] = struct{}{}
			podCandidates = append(podCandidates, candidate{key: key, host: Host{
				Name:    item.Name + ":" + portString,
				Address: net.JoinHostPort(item.Status.PodIP, portString),
				Age:     age,
			}})
		}
		if len(podCandidates) > 0 {
			candidates = append(candidates, podCandidates)
		}
	}
	v.discovery.prune(namespace, active)

	matches := make([]*Host, len(candidates))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(cap(v.discovery.slots), len(candidates)) {
		workers.Go(func() {
			for i := range jobs {
				for _, candidate := range candidates[i] {
					if ctx.Err() != nil {
						break
					}
					if v.discovery.valid(ctx, candidate.key, candidate.host.Address) {
						matches[i] = &candidate.host
						break
					}
				}
			}
		})
	}
queue:
	for i := range candidates {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break queue
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	hosts := make([]Host, 0, len(candidates))
	for _, match := range matches {
		if match != nil {
			hosts = append(hosts, *match)
		}
	}
	return hosts, nil
}

// Containers and ports retain declaration order.
// Loopback-only listeners cannot be discovered from a PodIP.
func candidatePorts(containers []v1Types.Container) []int32 {
	seen := make(map[int32]struct{})
	ports := make([]int32, 0)
	for _, container := range containers {
		for _, port := range container.Ports {
			if (port.Protocol != "" && port.Protocol != v1Types.ProtocolTCP) || port.ContainerPort < 1 || port.ContainerPort > 65535 {
				continue
			}
			if _, exists := seen[port.ContainerPort]; exists {
				continue
			}
			seen[port.ContainerPort] = struct{}{}
			ports = append(ports, port.ContainerPort)
		}
	}
	return ports
}
