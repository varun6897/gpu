package telemetry

import "time"

// Record represents one logical telemetry datapoint flowing through the system.
// It matches the shape of a row in the DCGM CSV file and is the canonical
// payload type produced by the streamer and consumed by collectors and the API.
type Record struct {
	Timestamp  time.Time `json:"timestamp"`
	MetricName string    `json:"metric_name"`
	GPUId      string    `json:"gpu_id"`
	Device     string    `json:"device"`
	UUID       string    `json:"uuid"`
	ModelName  string    `json:"modelName"`
	Hostname   string    `json:"hostname"`
	Container  string    `json:"container"`
	Pod        string    `json:"pod"`
	Namespace  string    `json:"namespace"`
	Value      string    `json:"value"`
	LabelsRaw  string    `json:"labels_raw"`
}

// GPUInfo represents summary metadata for a GPU, used by the API when listing GPUs.
// It intentionally exposes only uuid, device, and modelName.
type GPUInfo struct {
	UUID      string `json:"uuid"`
	Device    string `json:"device"`
	ModelName string `json:"modelName"`
}

