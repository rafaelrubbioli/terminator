package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

func main() {
	app := cli.App{
		Name: "OOM Terminator",
		Commands: []*cli.Command{
			{
				Name: "terminate",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Usage: "kube config file path, default is incluster config"},
					&cli.BoolFlag{Name: "local", Value: false, Usage: "use local config .kube/config file"},
					&cli.BoolFlag{Name: "dry-run", Value: false, Usage: "will not delete pods, only print when it reaches limit"},
					&cli.BoolFlag{Name: "debug", Value: false, Usage: "if set will log all steps"},

					&cli.StringFlag{Name: "namespace", Usage: "namespace to look for pods, if empty gets all namespaces"},
					&cli.StringSliceFlag{Name: "services", Usage: "services to get the pods from"},
					&cli.StringSliceFlag{Name: "deployments", Usage: "deployments to get pods from"},

					&cli.IntFlag{Name: "limit", Aliases: []string{"l"}, Value: 95, Usage: "memory usage percentage limit"},
					&cli.IntFlag{Name: "sleep", Aliases: []string{"t"}, Value: 1000, Usage: "duration in milliseconds to sleep between checks"},
					&cli.IntFlag{Name: "kill-sleep", Value: 1000, Usage: "duration in milliseconds to sleep after killing a pod"},
					&cli.IntFlag{Name: "kill-after", Value: 1, Usage: "amount of checks the pod needs to be over limit to be killed"},
				},
				Action: func(ctx *cli.Context) error {
					configFile := ctx.String("config")
					namespace := ctx.String("namespace")
					limit := ctx.Int("limit")
					services := ctx.StringSlice("services")
					deployments := ctx.StringSlice("deployments")
					dryRun := ctx.Bool("dry-run")
					sleep := time.Millisecond * time.Duration(ctx.Int("sleep"))
					killSleep := time.Millisecond * time.Duration(ctx.Int("kill-sleep"))
					killAfter := ctx.Int("kill-after")

					// local
					if ctx.Bool("local") {
						if home, err := os.UserHomeDir(); err == nil {
							configFile = path.Join(home, ".kube/config")
						}
					}

					logrus.SetLevel(logrus.ErrorLevel)
					if ctx.Bool("debug") {
						logrus.SetLevel(logrus.InfoLevel)
					}

					config, err := getConfig(configFile)
					if err != nil {
						return err
					}

					terminator, err := NewTerminator(config, dryRun)
					if err != nil {
						return err
					}

					fmt.Printf("Checking for pods")
					if namespace != "" {
						fmt.Printf(" at namespace %s", namespace)
					}
					if len(services) > 0 {
						fmt.Printf(" by services: %s", services)
					}
					if len(deployments) > 0 {
						fmt.Printf(" by deployments: %s", deployments)
					}

					return terminator.Terminate(ctx.Context, namespace, limit, killAfter, services, deployments, sleep, killSleep)
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

type Terminator interface {
	Terminate(ctx context.Context, namespace string, memoryLimit, killAfter int, serviceNames, deploymentNames []string, sleep, killSleep time.Duration) error
}

type terminator struct {
	clientset *kubernetes.Clientset
	metrics   *metrics.Clientset
	dryRun    bool
}

func NewTerminator(config *rest.Config, dryRun bool) (Terminator, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	mc, err := metrics.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return terminator{
		clientset: clientset,
		metrics:   mc,
		dryRun:    dryRun,
	}, nil
}

type overLimit struct {
	at    time.Time
	count int
}

func (t terminator) Terminate(ctx context.Context, namespace string, memoryLimit, killAfter int, serviceNames, deploymentNames []string, sleep, killSleep time.Duration) error {
	podsToKill := make(map[string]*overLimit)
	for {
		killed := false
		pods, err := t.getPods(ctx, namespace, serviceNames, deploymentNames)
		if err != nil {
			return err
		}

		logrus.Infof("found %d pods", len(pods.Items))

		for _, pod := range pods.Items {
			if len(pod.Spec.Containers) == 0 || pod.Status.Phase != "Running" || killed {
				continue
			}

			limit := pod.Spec.Containers[0].Resources.Limits.Memory()
			podMetrics, err := t.metrics.MetricsV1beta1().PodMetricses(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					logrus.Infof("Pod %s has no metrics", pod.Name)
					continue
				}
				return err
			}

			if len(podMetrics.Containers) == 0 {
				continue
			}

			using := podMetrics.Containers[0].Usage.Memory()
			percentage := float64(using.Value()) / float64(limit.Value()) * 100
			logrus.Infof("pod < %s > (%s/%s) = %.f%%", pod.Name, using.String(), limit.String(), percentage)

			if percentage >= float64(memoryLimit) {
				if over, ok := podsToKill[pod.Name]; ok {
					over.count = over.count + 1
				} else {
					podsToKill[pod.Name] = &overLimit{at: time.Now()}
				}

				log.Printf(" pod < %s > (%s/%s = %.f%% over the memory limit)", pod.Name, using.String(), limit.String(), percentage)
				if podsToKill[pod.Name].count >= killAfter {
					log.Printf("Deleting pod < %s > (has exceeded memory limit for %d checks)", pod.Name, killAfter)
					if !t.dryRun {
						err := t.clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: pod.DeletionGracePeriodSeconds})
						if err != nil {
							return err
						}
					}
					time.Sleep(killSleep)
					delete(podsToKill, pod.Name)
					killed = true
				}
			}
		}

		// expire old pods that were over limit, but arent anymore or were deleted
		for pod, over := range podsToKill {
			if time.Since(over.at) > killSleep*time.Duration(over.count+1) {
				logrus.Infof("Pod %s is not over limit anymore or has already terminated", pod)
				delete(podsToKill, pod)
			}
		}

		time.Sleep(sleep)
	}
}

func (t terminator) getPods(ctx context.Context, namespace string, serviceNames, deploymentNames []string) (*v1.PodList, error) {
	if len(serviceNames) == 0 && len(deploymentNames) == 0 {
		return t.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: 10})
	}

	deploymentsClient := t.clientset.AppsV1().Deployments(namespace)
	pods := new(v1.PodList)
	for _, name := range serviceNames {
		service, err := t.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				logrus.Errorf("service %s not found", service.Name)
				continue
			}
			return nil, err
		}

		set := labels.Set(service.Spec.Selector)
		servicePods, err := t.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: set.AsSelector().String()})
		if err != nil {
			return nil, err
		}

		logrus.Infof("service %s has %d pods", name, len(servicePods.Items))
		pods.Items = append(pods.Items, servicePods.Items...)
	}

	for _, name := range deploymentNames {
		deployment, err := deploymentsClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				logrus.Errorf("deployment %s not found", deployment.Name)
				continue
			}
			return nil, err
		}

		set := labels.Set(deployment.Spec.Selector.MatchLabels)
		deploymentPods, err := t.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: set.AsSelector().String()})
		if err != nil {
			return nil, err
		}

		running := 0
		for _, pod := range deploymentPods.Items {
			if pod.Status.Phase == "Running" {
				running = running + 1
			}
		}

		if running >= int(*deployment.Spec.Replicas) {
			logrus.Infof("deployment %s has %d pods", name, len(deploymentPods.Items))
			pods.Items = append(pods.Items, deploymentPods.Items...)
		} else {
			logrus.Infof("skipping %s, not all pods are running", name)
		}
	}

	return pods, nil
}

func getConfig(configFile string) (*rest.Config, error) {
	if configFile != "" {
		return clientcmd.BuildConfigFromFlags("", configFile)
	}

	return rest.InClusterConfig()
}
