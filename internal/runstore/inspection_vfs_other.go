//go:build !cgo || (!linux && !darwin)

package runstore

func inspectionSQLiteOptions() (string, error) {
	return "", failure("storage_unavailable", "nonmutating run inspection requires a Linux or Darwin CGO build")
}
