# Command line arguments

```console
Usage of :
      --annotations-prefix string         Prefix of the ingress annotations. (default "nginx.ingress.kubernetes.io")
      --apiserver-host string             The address of the Kubernetes Apiserver to connect to in the format of protocol://address:port, e.g., http://localhost:8080. If not specified, the assumption is that the binary runs inside a Kubernetes cluster and local discovery is attempted.
      --configmap string                  Name of the ConfigMap that contains the custom configuration to use
      --default-backend-service string    Service used to serve a 404 page for the default backend. Takes the form
		namespace/name. The controller uses the first node port of this Service for
		the default backend.
      --default-ssl-certificate string    Name of the secret
		that contains a SSL certificate to be used as default for a HTTPS catch-all server.
		Takes the form <namespace>/<secret name>.
      --disable-node-list                 Disable querying nodes. If --force-namespace-isolation is true, this should also be set. (DEPRECATED)
      --election-id string                Election id to use for status update. (default "ingress-controller-leader")
      --enable-ssl-chain-completion       Defines if the nginx ingress controller should check the secrets for missing intermediate CA certificates.
		If the certificate contain issues chain issues is not possible to enable OCSP.
		Default is true. (default true)
      --force-namespace-isolation         Force namespace isolation. This flag is required to avoid the reference of secrets or
		configmaps located in a different namespace than the specified in the flag --watch-namespace.
      --ingress-class string              Name of the ingress class to route through this controller.
      --kong-url string                   The address of the Kong Admin URL to connect to in the format of protocol://address:port, e.g., http://localhost:8080
      --kubeconfig string                 Path to kubeconfig file with authorization and master location information.
      --profiling                         Enable profiling via web interface host:port/debug/pprof/ (default true)
      --publish-service string            Service fronting the ingress controllers. Takes the form namespace/name.
		The controller will set the endpoint records on the ingress objects to reflect those on the service.
      --report-node-internal-ip-address   Defines if the nodes IP address to be returned in the ingress status should be the internal instead of the external IP address
      --sync-period duration              Relist and confirm cloud resources this often. Default is 10 minutes (default 10m0s)
      --sync-rate-limit float32           Define the sync frequency upper limit (default 0.3)
      --update-status                     Indicates if the
		ingress controller should update the Ingress status IP/hostname. Default is true (default true)
      --update-status-on-shutdown         Indicates if the
		ingress controller should update the Ingress status IP/hostname when the controller
		is being stopped. Default is true (default true)
      --version                           Shows release information about the NGINX Ingress controller
      --watch-namespace string            Namespace to watch for Ingress. Default is to watch all namespaces
```
