package utils

import (
	"fmt"
	"strings"

	networkoperatorv1alpha1 "github.com/rh-waterford-et/ac3_networkoperator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

const maxResourceNameLength = 63

func TruncateNameIfNeeded(name string) string {
	if errors := validation.IsQualifiedName(name); len(errors) != 0 {
		truncatedName := name[:maxResourceNameLength]
		truncatedName = strings.TrimRight(truncatedName, "-")
		return truncatedName
	} else {
		return name
	}
}

func SentenceCase(s string) string {
	return strings.ToUpper(s[:1]) + s[1:]
}

func IsMultiClusterNetworkInstalled() (bool, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return false, fmt.Errorf("%w", err)
	}

	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return false, fmt.Errorf("%w", err)
	}

	restMapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	if err != nil {
		return false, fmt.Errorf("%w", err)
	}

	gvk := networkoperatorv1alpha1.GroupVersion.WithKind("MultiClusterNetwork")
	_, err = restMapper.RESTMapping(
		schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind},
		gvk.Version,
	)
	if err == nil {
		return true, nil
	}

	if meta.IsNoMatchError(err) {
		return false, nil
	}

	return false, fmt.Errorf("%w", err)
}
