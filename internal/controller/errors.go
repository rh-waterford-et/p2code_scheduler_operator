package controller

type MisconfiguredManifestError struct {
	message string
}

type ManifestWorkFailedError struct {
	message string
}

type ManifestWorkNotReadyError struct {
	message string
}

type ResourceNotFoundError struct {
	message string
}

func (e MisconfiguredManifestError) Error() string {
	return e.message
}

func (e ManifestWorkFailedError) Error() string {
	return e.message
}

func (e ManifestWorkNotReadyError) Error() string {
	return e.message
}

func (e ResourceNotFoundError) Error() string {
	return e.message
}
