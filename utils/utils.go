package utils

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
