package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/kcp"

	gitopsv1alpha1 "github.com/redhat-appstudio/managed-gitops/backend-shared/apis/managed-gitops/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"

	datav1alpha1 "github.com/kcp-dev/controller-runtime-example/api/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/logicalcluster/v2"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

func init() {
	klog.InitFlags(flag.CommandLine)
	if err := flag.Lookup("v").Value.Set("5"); err != nil {
		panic(err)
	}
}

func main() {
	fmt.Println("Fetching a simple gitops deployment from user workspace")
	client, err := NewVirtualWorkspaceClient()
	if err != nil {
		log.Fatalf("failed to create VW client: %v", err)
	}

	wsName := "root:users:ti:rg:rh-sso-cbanavik-kcp-redhat-com:user-28620"

	ctx := logicalcluster.WithCluster(context.Background(), logicalcluster.New(wsName))

	gitopsDepl := &gitopsv1alpha1.GitOpsDeployment{}
	if err := client.Get(ctx, types.NamespacedName{Name: "gitops-test", Namespace: "test"}, gitopsDepl); err != nil {
		log.Fatalf("failed to get GitOpsDeployment: %v", err)
	}

	fmt.Printf("GitOps Deployment found: %+v", gitopsDepl)

}

// NewVirtualWorkspaceClient returns a client that can access the a virtual workspace
func NewVirtualWorkspaceClient() (client.Client, error) {
	restConfig, err := GetRESTConfig()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add client go to scheme: %v", err)
	}
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add %s to scheme: %v", tenancyv1alpha1.SchemeGroupVersion, err)
	}
	if err := datav1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add %s to scheme: %v", datav1alpha1.GroupVersion, err)
	}
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add %s to scheme: %v", apisv1alpha1.SchemeGroupVersion, err)
	}

	httpClient, err := kcp.ClusterAwareHTTPClient(restConfig)
	if err != nil {
		fmt.Printf("error creating HTTP client: %v", err)
		return nil, err
	}
	apiExportClient, err := client.New(restConfig, client.Options{Scheme: scheme, HTTPClient: httpClient})
	if err != nil {
		return nil, fmt.Errorf("error creating APIExport client: %w", err)
	}

	err = gitopsv1alpha1.AddToScheme(scheme)
	if err != nil {
		return nil, fmt.Errorf("failed to add %s to scheme: %v", gitopsv1alpha1.GroupVersion, err)
	}

	// Try up to 120 seconds for the virtual workspace URL to become available.
	err = wait.PollImmediate(time.Second, time.Second*120, func() (bool, error) {
		var err error
		restConfig, err = restConfigForAPIExport(ctx, restConfig, apiExportClient, "gitopsrvc-backend-shared")
		if err != nil {
			fmt.Printf("error looking up virtual workspace URL: '%v', retrying in 1 second.\n", err)
			return false, nil
		}
		return true, nil
	})

	if err != nil {
		return nil, fmt.Errorf("virtual workspace URL never became available")
	}

	httpClient, err = kcp.ClusterAwareHTTPClient(restConfig)
	if err != nil {
		fmt.Printf("error creating HTTP client: %v\n", err)
		return nil, err
	}

	return client.New(restConfig, client.Options{Scheme: scheme, HTTPClient: httpClient})
}

func GetRESTConfig() (*rest.Config, error) {
	res, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}

	// Use non-standard rate limiting values
	// TODO: GITOPSRVCE-68 - These values are way too high, need to look at request.go in controller-runtime.
	res.QPS = 100
	res.Burst = 250
	return res, nil
}

// restConfigForAPIExport returns a *rest.Config properly configured to communicate with the endpoint for the
// APIExport's virtual workspace.
func restConfigForAPIExport(ctx context.Context, cfg *rest.Config, apiExportClient client.Client, apiExportName string) (*rest.Config, error) {

	var apiExport apisv1alpha1.APIExport

	if apiExportName != "" {
		if err := apiExportClient.Get(ctx, types.NamespacedName{Name: apiExportName}, &apiExport); err != nil {
			return nil, fmt.Errorf("error getting APIExport %q: %w", apiExportName, err)
		}
	} else {
		exports := &apisv1alpha1.APIExportList{}
		if err := apiExportClient.List(ctx, exports); err != nil {
			return nil, fmt.Errorf("error listing APIExports: %w", err)
		}
		if len(exports.Items) == 0 {
			return nil, fmt.Errorf("no APIExport found")
		}
		if len(exports.Items) > 1 {
			return nil, fmt.Errorf("more than one APIExport found")
		}
		apiExport = exports.Items[0]
	}

	if len(apiExport.Status.VirtualWorkspaces) < 1 {
		return nil, fmt.Errorf("APIExport %q status.virtualWorkspaces is empty", apiExportName)
	}

	cfg = rest.CopyConfig(cfg)
	// TODO: GITOPSRVCE-204 - implement sharding of virtual workspaces
	cfg.Host = apiExport.Status.VirtualWorkspaces[0].URL

	return cfg, nil
}
