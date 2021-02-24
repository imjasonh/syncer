package main // import "github.com/imjasonh/hybrid"

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const (
	resyncPeriod  = time.Hour
	labelSelector = "cluster=my-cluster"
)

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
		//		o.LabelSelector = labelSelector
	})

	// Create a client to modify "to"
	toClient, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	for _, gvrstr := range []string{
		"deployments.v1.apps",
		// TODO: handle gvrs without group (configmaps)
	} {
		gvr, _ := schema.ParseResourceArg(gvrstr)
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

				if _, err := toClient.Resource(*gvr).Namespace(*to).Create(ctx, u, metav1.CreateOptions{}); err != nil {
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

				if _, err := toClient.Resource(*gvr).Namespace(*to).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
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

				if err := toClient.Resource(*gvr).Namespace(*to).Delete(ctx, u.GetName(), metav1.DeleteOptions{}); err != nil {
					log.Printf("ERROR deleting: %v", err)
				}
			},
		})
	}
	stopCh := make(chan struct{})
	dsif.Start(stopCh)
	<-stopCh
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
