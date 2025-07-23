package controller

type MisconfiguredManifestError struct {
	message string
}

type ManifestWorkFailedError struct {
	message string
}

type ManifestWorkNotReady struct {
	message string
}

type ResourceNotFound struct {
	message string
}

func (e MisconfiguredManifestError) Error() string {
	return e.message
}

func (e ManifestWorkFailedError) Error() string {
	return e.message
}

func (e ManifestWorkNotReady) Error() string {
	return e.message
}

func (e ResourceNotFound) Error() string {
	return e.message
}
