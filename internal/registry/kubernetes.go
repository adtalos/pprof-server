package registry

import (
	"context"
	"strconv"
	"time"

	v1Types "k8s.io/api/core/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

type kubernetesRegistry struct {
	core               v1.CoreV1Interface
	namespaceInterface v1.NamespaceInterface
}

func NewKubernetesRegistry(client *kubernetes.Clientset) Registry {
	core := client.CoreV1()
	return kubernetesRegistry{
		core:               core,
		namespaceInterface: core.Namespaces(),
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
				if port.Name != "http" || port.Protocol != v1Types.ProtocolTCP {
					continue
				}
				hosts = append(hosts, Host{
					Name:    item.Name,
					Address: item.Status.PodIP + ":" + strconv.FormatInt(int64(port.ContainerPort), 10),
					Age:     now.Sub(item.Status.StartTime.Time),
				})
			}
		}

	}
	return hosts, nil
}
