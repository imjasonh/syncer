package main // import "github.com/imjasonh/syncer"

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const resyncPeriod = time.Hour

var (
	from = flag.String("from", "from", "Namespace to mirror from")
	to   = flag.String("to", "to", "Namespace to mirror to")
)

func main() {
	flag.Parse()
	ctx := context.Background()

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	if *to == "" || *from == "" {
		log.Fatalf("--to (%q) and --from (%q) must be provided", *to, *from)
	}
	for _, s := range []string{*to, *from} {
		if _, err := clientset.CoreV1().Namespaces().Get(ctx, s, metav1.GetOptions{}); err != nil {
			log.Fatalf("Getting namespace %q: %v", s, err)
		}
	}

	dyn := dynamic.NewForConfigOrDie(config)
	dsif := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, resyncPeriod, *from, func(o *metav1.ListOptions) {
		// o.LabelSelector = labelSelector
	})

	// Create a client to modify "to"
	toClient, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		log.Fatal(err)
	}
	rs, err := dc.ServerResources()
	if err != nil {
		log.Fatal(err)
	}
	var gvrstrs []string
	for _, r := range rs {
		// v1 -> v1.
		// apps/v1 -> v1.apps
		// tekton.dev/v1beta1 -> v1beta1.tekton.dev
		parts := strings.SplitN(r.GroupVersion, "/", 2)
		vr := parts[0] + "."
		if len(parts) == 2 {
			vr = parts[1] + "." + parts[0]
		}
		for _, ai := range r.APIResources {
			if strings.Contains(ai.Name, "/") {
				// foo/status, pods/exec, namespace/finalize, etc.
				continue
			}
			if !ai.Namespaced {
				// Ignore cluster-scoped things.
				continue
			}
			if !contains(ai.Verbs, "watch") {
				log.Printf("resource %s %s is not watchable: %v", vr, ai.Name, ai.Verbs)
				continue
			}
			gvrstrs = append(gvrstrs, fmt.Sprintf("%s.%s", ai.Name, vr))
		}
	}
	for _, gvrstr := range gvrstrs {
		gvr, _ := schema.ParseResourceArg(gvrstr)

		if _, err := dsif.ForResource(*gvr).Lister().List(labels.Everything()); err != nil {
			log.Println("Failed to list all %q: %v", gvrstr, err)
			continue
		}

		dsif.ForResource(*gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				u, err := interfaceToUnstructured(obj)
				if err != nil {
					log.Printf("ERROR: %v", err)
					return
				}
				u.SetNamespace(*to)
				u.SetResourceVersion("")
				u.SetUID("")

				log.Printf("Creating gvr (%+v), name=%q namespace=%q", gvr, u.GetName(), u.GetNamespace())
				if _, err := toClient.Resource(*gvr).Namespace(*to).Create(ctx, u, metav1.CreateOptions{}); k8serrors.IsAlreadyExists(err) {
					// Try to update it.
					log.Printf("Failed to create %q because it already exists; trying to update...", u.GetName())
					if _, err := toClient.Resource(*gvr).Namespace(*to).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
						log.Printf("ERROR updating after failed create: %v", err)
					}
				} else if err != nil {
					log.Printf("ERROR creating: %v", err)
				}
			},
			UpdateFunc: func(_, new interface{}) {
				u, err := interfaceToUnstructured(new)
				if err != nil {
					log.Printf("ERROR: %v", err)
					return
				}
				u.SetNamespace(*to)
				u.SetResourceVersion("")
				u.SetUID("")

				log.Printf("Updating gvr (%+v), name=%q namespace=%q", gvr, u.GetName(), u.GetNamespace())
				if _, err := toClient.Resource(*gvr).Namespace(*to).Update(ctx, u, metav1.UpdateOptions{}); k8serrors.IsNotFound(err) {
					// Try to create it.
					log.Printf("Failed to update %q because it was not found; trying to create...", u.GetName())
					if _, err := toClient.Resource(*gvr).Namespace(*to).Create(ctx, u, metav1.CreateOptions{}); err != nil {
						log.Printf("ERROR creating after failed update: %v", err)
					}
				} else if err != nil {
					log.Printf("ERROR updating: %v", err)
				}
			},
			DeleteFunc: func(obj interface{}) {
				u, err := interfaceToUnstructured(obj)
				if err != nil {
					log.Printf("ERROR: %v", err)
					return
				}
				u.SetNamespace(*to)

				log.Printf("Deleting gvr (%+v), name=%q namespace=%q", gvr, u.GetName(), u.GetNamespace())
				if err := toClient.Resource(*gvr).Namespace(*to).Delete(ctx, u.GetName(), metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) && !k8serrors.IsGone(err) {
					log.Printf("ERROR deleting: %v", err)
				}
			},
		})
		log.Printf("Set up informer for %v", gvr)
	}
	stopCh := make(chan struct{})
	dsif.Start(stopCh)
	<-stopCh
}

func contains(ss []string, s string) bool {
	for _, n := range ss {
		if n == s {
			return true
		}
	}
	return false
}

func interfaceToUnstructured(i interface{}) (*unstructured.Unstructured, error) {
	b, err := json.Marshal(i)
	if err != nil {
		return nil, err
	}

	o, _, err := unstructured.UnstructuredJSONScheme.Decode(b, nil, nil)
	if err != nil {
		return nil, err
	}

	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		return nil, err
	}

	return &unstructured.Unstructured{Object: m}, nil
}
