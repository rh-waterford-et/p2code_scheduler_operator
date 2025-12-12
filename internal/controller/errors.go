package controller

const fetchFailure = "Failed to fetch P2CodeSchedulingManifest"
const updateFailure = "Failed to update P2CodeSchedulingManifest status"
const configurationIssue = "There is a configuration issue in the P2CodeSchedulingManifest instance"

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
