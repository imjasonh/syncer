# Install the syncer

(assuming a local KinD cluster)

```
$ KO_DOCKER_REPO=kind.local
$ ko apply -f controller.yaml
```

# Create a deployment in `from`

```
$ kubectl apply -f deployment.yaml
deployment.apps/foo configured
```

# See it's created in `from` and `to`

```
$ kubectl get deploy -n from
NAME   READY   UP-TO-DATE   AVAILABLE   AGE
foo    1/1     1            1           69s
$ kubectl get deploy -n to
NAME   READY   UP-TO-DATE   AVAILABLE   AGE
foo    1/1     1            1           69s
```

Nice.

# Update the deployment in `from`

```
$ kubectl edit deploy foo -n from
< modify replicas=3 >
```

# See it's updated in `from` and `to`

```
$ kubectl get deploy -n from
NAME   READY   UP-TO-DATE   AVAILABLE   AGE
foo    3/3     3            3           69s
$ kubectl get deploy -n to
NAME   READY   UP-TO-DATE   AVAILABLE   AGE
foo    3/3     3            3           69s
```

# Delete the deployment in `from`

```
$ kubectl delete deploy foo -n from
deployment.apps "foo" deleted
```

# See it's deleted in `to`

```
$ kubectl get deploy -n from
No resources found in to namespace.
$ kubectl get deploy -n to
No resources found in to namespace.
```

🎉
