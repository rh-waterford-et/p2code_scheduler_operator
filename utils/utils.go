package utils

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

func IsCRDInstalled(restMapper meta.RESTMapper, groupVersionKind schema.GroupVersionKind) (bool, error) {
	_, err := restMapper.RESTMapping(
		schema.GroupKind{Group: groupVersionKind.Group, Kind: groupVersionKind.Kind},
		groupVersionKind.Version,
	)
	if err == nil {
		return true, nil
	}

	if meta.IsNoMatchError(err) {
		return false, nil
	}

	return false, err
}

func CreateRESTMapper() (meta.RESTMapper, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}

	return apiutil.NewDynamicRESTMapper(cfg, httpClient)
}
