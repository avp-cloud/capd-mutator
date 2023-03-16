package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
	dockerclient "github.com/docker/docker/client"
	"github.com/ghodss/yaml"
	"github.com/kubernetes-client/go/kubernetes/config/api"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	kubeconfig       = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	host             = flag.String("host", "", "docker host ip")
	namespace        = flag.String("namespace", "", "namespace of deployment")
	suffix           = flag.String("suffix", "", "suffix to be added to cluster name for the mutated kubeconfig secret")
	disableTlsVerify = flag.Bool("disableTlsVerify", true, "disable tls verification in mutated kubeconfig")
	dockerPort       = flag.String("docker-port", "2375", "docker api port")
)

func main() {
	flag.Parse()
	var err error

	dc, err := getDockerClient("tcp://" + *host + ":" + *dockerPort)
	if err != nil {
		log.Fatal(err.Error())
	}
	defer dc.Close()

	var config *rest.Config
	if *kubeconfig == "" {
		config, err = rest.InClusterConfig()
		if err != nil {
			log.Fatal(err.Error())
		}
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	}

	if err != nil {
		log.Fatal(err.Error())
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err.Error())
	}

	watcher, err := clientset.CoreV1().Secrets(*namespace).Watch(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}

	for event := range watcher.ResultChan() {
		sec := event.Object.(*v1.Secret)
		if strings.Contains(sec.Name, "-kubeconfig") {
			cluName := strings.ReplaceAll(sec.Name, "-kubeconfig", "")
			switch event.Type {
			case watch.Added, watch.Modified:
				fmt.Printf("Detected new kubeconfig secret %s\n", sec.Name)
				p, err := getContainerHostPort(dc, cluName+"-lb")
				if err != nil {
					fmt.Printf("failed to get container port mapping err:%v\n", err.Error())
					continue
				}
				kc := api.Config{}
				err = yaml.Unmarshal(sec.Data["value"], &kc)
				if err != nil {
					fmt.Printf("failed to parse kubeconfg err:%v\n", err.Error())
					continue
				}
				kc.Clusters[0].Cluster.Server = "https://" + *host + ":" + p
				if *disableTlsVerify {
					kc.Clusters[0].Cluster.CertificateAuthorityData = nil
					kc.Clusters[0].Cluster.InsecureSkipTLSVerify = true
				}
				kcBytes, _ := yaml.Marshal(kc)

				var newSec *v1.Secret
				newSec, err = clientset.CoreV1().Secrets(*namespace).Get(context.Background(), cluName+*suffix, metav1.GetOptions{})
				if err != nil {
					// already exists
					newSec.Data = map[string][]byte{"value": kcBytes}
					_, err = clientset.CoreV1().Secrets(*namespace).Update(context.Background(), newSec, metav1.UpdateOptions{})
					if err != nil {
						fmt.Printf("failed to mutate kubeconfig secret err:%v\n", err.Error())
					} else {
						fmt.Printf("Mutated updated kubeconfig secret %s\n", cluName+*suffix)
					}
				} else if apierrors.IsNotFound(err) {
					newSec = &v1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      cluName + *suffix,
							Namespace: *namespace,
						},
						Data: map[string][]byte{"value": kcBytes},
					}

					_, err = clientset.CoreV1().Secrets(*namespace).Create(context.Background(), newSec, metav1.CreateOptions{})
					if err != nil {
						fmt.Printf("failed to mutate kubeconfig secret err:%v\n", err.Error())
					} else {
						fmt.Printf("Mutated new kubeconfig secret %s\n", cluName+*suffix)
					}
				} else {
					fmt.Printf("failed to mutate kubeconfig secret err:%v\n", err.Error())
				}
			case watch.Deleted:
				fmt.Printf("Detected deleted kubeconfig secret %s\n", sec.Name)
				err = clientset.CoreV1().Secrets(*namespace).Delete(context.Background(), cluName+*suffix, metav1.DeleteOptions{})
				if err != nil {
					fmt.Printf("failed to delete mutated kubeconfig secret err:%v\n", err.Error())
				} else {
					fmt.Printf("Deleted mutated kubeconfig secret %s\n", cluName+*suffix)
				}
			}
		}
	}

}

// getDockerClient returns a docker client for given docker host
func getDockerClient(host string) (*dockerclient.Client, error) {
	return dockerclient.NewClientWithOpts(dockerclient.WithHost(host), dockerclient.WithAPIVersionNegotiation())
}

// getContainerHostPort ...
func getContainerHostPort(cli *dockerclient.Client, container string) (string, error) {
	ctx := context.Background()
	ls, err := cli.ContainerList(ctx, dockertypes.ContainerListOptions{})
	if err != nil {
		return "", err
	}
	cID := ""
	for i := range ls {
		for n := range ls[i].Names {
			if ls[i].Names[n] == "/"+container {
				cID = ls[i].ID
			}
		}
	}
	cjson, err := cli.ContainerInspect(ctx, cID)
	if err != nil {
		return "", err
	}
	pt := cjson.NetworkSettings.Ports["6443/tcp"]
	if pt != nil {
		return pt[0].HostPort, nil
	}
	return "", fmt.Errorf("port mapping not found for container %v, port 6443", container)
}
