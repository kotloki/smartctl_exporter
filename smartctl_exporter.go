package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"
)

const version = "0.0.3"

type Device struct {
	Name         string
	Type         string
	ModelFamily  string
	ModelName    string
	SerialNumber string
	UserCapacity string
	BusDevice    string
	MegaraidID   string
}

var (
	labelNames = []string{
		"drive",
		"type",
		"model_family",
		"model_name",
		"serial_number",
		"user_capacity",
	}
	devices        = make(map[string]*Device)
	metrics        = make(map[string]*prometheus.GaugeVec)
	satTypes       = []string{"sat", "usbjmicron", "usbprolific", "usbsunplus"}
	nvmeTypes      = []string{"nvme", "sntasmedia", "sntjmicron", "sntrealtek"}
	scsiTypes      = []string{"scsi"}
	megaraidRegexp = regexp.MustCompile(`(sat\+)?(megaraid,\d+)`)
	mutex          = &sync.Mutex{}
)

func runSmartctlCmd(args []string) ([]byte, int, error) {
	cmd := exec.Command("smartctl", args...)
	output, err := cmd.CombinedOutput()
	exitCode := cmd.ProcessState.ExitCode()
	if err != nil {
		log.Printf("WARNING: Command '%s' returned exit code %d. Output: '%s'", strings.Join(cmd.Args, " "), exitCode, string(output))
	}
	return output, exitCode, err
}

func getDrives() map[string]*Device {
	disks := make(map[string]*Device)
	output, _, err := runSmartctlCmd([]string{"--scan-open", "--json=c"})
	if err != nil {
		log.Println("Error scanning devices:", err)
		return disks
	}

	var result struct {
		Devices []struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			OpenError string `json:"open_error"`
		} `json:"devices"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		log.Println("Error parsing JSON:", err)
		return disks
	}

	for _, device := range result.Devices {
		if device.OpenError != "" {
			continue
		}
		dev := device.Name
		typ := device.Type

		if megaraidRegexp.MatchString(typ) {
			diskAttrs := getMegaraidDeviceInfo(dev, typ)
			if diskAttrs == nil {
				continue
			}
			diskAttrs.Type = getMegaraidDeviceType(dev, typ)
			diskAttrs.BusDevice = dev
			diskAttrs.MegaraidID = getMegaraidDeviceID(typ)
			dev = diskAttrs.MegaraidID
			disks[dev] = diskAttrs
		} else {
			diskAttrs := getDeviceInfo(dev)
			diskAttrs.Type = typ
			disks[dev] = diskAttrs
		}
		log.Printf("Discovered device %s with attributes %+v\n", dev, disks[dev])
	}

	return disks
}

func getDeviceInfo(dev string) *Device {
	output, _, err := runSmartctlCmd([]string{"-i", "--json=c", dev})
	if err != nil {
		log.Println("Error getting device info:", err)
		return &Device{}
	}

	var result struct {
		ModelFamily  string `json:"model_family"`
		ModelName    string `json:"model_name"`
		SerialNumber string `json:"serial_number"`
		UserCapacity struct {
			Bytes int64 `json:"bytes"`
		} `json:"user_capacity"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		log.Println("Error parsing device info JSON:", err)
		return &Device{}
	}

	userCapacity := "Unknown"
	if result.UserCapacity.Bytes > 0 {
		userCapacity = strconv.FormatInt(result.UserCapacity.Bytes, 10)
	}

	return &Device{
		ModelFamily:  result.ModelFamily,
		ModelName:    result.ModelName,
		SerialNumber: result.SerialNumber,
		UserCapacity: userCapacity,
	}
}

func getMegaraidDeviceInfo(dev, typ string) *Device {
	megaraidID := getMegaraidDeviceID(typ)
	if megaraidID == "" {
		return nil
	}
	output, _, err := runSmartctlCmd([]string{"-i", "--json=c", "-d", megaraidID, dev})
	if err != nil {
		log.Println("Error getting MegaRAID device info:", err)
		return nil
	}

	var result struct {
		ModelFamily    string `json:"model_family"`
		ModelName      string `json:"model_name"`
		SerialNumber   string `json:"serial_number"`
		UserCapacity   struct {
			Bytes int64 `json:"bytes"`
		} `json:"user_capacity"`
		ScsiModelName string `json:"scsi_model_name"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		log.Println("Error parsing MegaRAID device info JSON:", err)
		return nil
	}

	modelName := result.ModelName
	if result.ScsiModelName != "" {
		modelName = result.ScsiModelName
	}

	userCapacity := "Unknown"
	if result.UserCapacity.Bytes > 0 {
		userCapacity = strconv.FormatInt(result.UserCapacity.Bytes, 10)
	}

	return &Device{
		ModelFamily:  result.ModelFamily,
		ModelName:    modelName,
		SerialNumber: result.SerialNumber,
		UserCapacity: userCapacity,
	}
}

func getMegaraidDeviceType(dev, typ string) string {
	megaraidID := getMegaraidDeviceID(typ)
	if megaraidID == "" {
		return "unknown"
	}
	output, _, err := runSmartctlCmd([]string{"-i", "--json=c", "-d", megaraidID, dev})
	if err != nil {
		log.Println("Error getting MegaRAID device type:", err)
		return "unknown"
	}

	var result struct {
		Device struct {
			Protocol string `json:"protocol"`
		} `json:"device"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		log.Println("Error parsing MegaRAID device type JSON:", err)
		return "unknown"
	}

	if result.Device.Protocol == "ATA" {
		return "sat"
	} else if result.Device.Protocol == "SCSI" {
		return "scsi"
	}
	return "unknown"
}

func getMegaraidDeviceID(typ string) string {
	matches := megaraidRegexp.FindStringSubmatch(typ)
	if len(matches) >= 3 {
		return matches[2]
	}
	return ""
}

func collect() {
	mutex.Lock()
	defer mutex.Unlock()

	for drive, device := range devices {
		typ := device.Type
		var attrs map[string]float64

		if device.MegaraidID != "" {
			attrs = smartMegaraid(device.BusDevice, device.MegaraidID)
		} else if contains(satTypes, typ) {
			attrs = smartSat(drive)
		} else if contains(nvmeTypes, typ) {
			attrs = smartNvme(drive)
		} else if contains(scsiTypes, typ) {
			attrs = smartScsi(drive)
		} else {
			continue
		}

		for key, value := range attrs {
			metricName := sanitizeMetricName("smartctl_" + key)
			if _, exists := metrics[metricName]; !exists {
				desc := key
				metrics[metricName] = prometheus.NewGaugeVec(
					prometheus.GaugeOpts{
						Name: metricName,
						Help: desc,
					},
					labelNames,
				)
				prometheus.MustRegister(metrics[metricName])
			}

			metrics[metricName].With(prometheus.Labels{
				"drive":         drive,
				"type":          typ,
				"model_family":  device.ModelFamily,
				"model_name":    device.ModelName,
				"serial_number": device.SerialNumber,
				"user_capacity": device.UserCapacity,
			}).Set(value)
		}
	}
}

func smartSat(dev string) map[string]float64 {
	output, _, err := runSmartctlCmd([]string{"-A", "-H", "-d", "sat", "--json=c", dev})
	if err != nil {
		log.Println("Error running smartctl for SAT:", err)
		return nil
	}

	var result struct {
		AtaSmartAttributes struct {
			Table []struct {
				ID    int    `json:"id"`
				Name  string `json:"name"`
				Value int    `json:"value"`
				Raw   struct {
					String string `json:"string"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		SmartStatus struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		log.Println("Error parsing SAT JSON:", err)
		return nil
	}

	attributes := make(map[string]float64)
	for _, attr := range result.AtaSmartAttributes.Table {
		name := attr.Name
		value := float64(attr.Value)
		rawValue := parseRawValue(attr.Raw.String)

		attributes[name] = value
		if rawValue != nil {
			attributes[name+"_raw"] = *rawValue
		}
	}

	attributes["smart_passed"] = boolToFloat(result.SmartStatus.Passed)
	return attributes
}

func smartNvme(dev string) map[string]float64 {
	output, _, err := runSmartctlCmd([]string{"-A", "-H", "-d", "nvme", "--json=c", dev})
	if err != nil {
		log.Println("Error running smartctl for NVMe:", err)
		return nil
	}

	var result struct {
		NvmeSmartHealthInformationLog map[string]interface{} `json:"nvme_smart_health_information_log"`
		SmartStatus                   struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		log.Println("Error parsing NVMe JSON:", err)
		return nil
	}

	attributes := make(map[string]float64)
	for key, value := range result.NvmeSmartHealthInformationLog {
		switch v := value.(type) {
		case float64:
			attributes[key] = v
		case int:
			attributes[key] = float64(v)
		case []interface{}:
			for i, sensorValue := range v {
				if val, ok := sensorValue.(float64); ok {
					attributes[fmt.Sprintf("%s_sensor%d", key, i+1)] = val
				}
			}
		}
	}

	attributes["smart_passed"] = boolToFloat(result.SmartStatus.Passed)
	return attributes
}

func smartScsi(dev string) map[string]float64 {
	output, _, err := runSmartctlCmd([]string{"-A", "-H", "-d", "scsi", "--json=c", dev})
	if err != nil {
		log.Println("Error running smartctl for SCSI:", err)
		return nil
	}

	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Println("Error parsing SCSI JSON:", err)
		return nil
	}

	attributes := make(map[string]float64)
	for key, value := range result {
		switch v := value.(type) {
		case float64:
			attributes[key] = v
		case int:
			attributes[key] = float64(v)
		case map[string]interface{}:
			for subKey, subValue := range v {
				if val, ok := subValue.(float64); ok {
					attributes[key+"_"+subKey] = val
				} else if valInt, ok := subValue.(int); ok {
					attributes[key+"_"+subKey] = float64(valInt)
				}
			}
		}
	}

	if smartStatus, ok := result["smart_status"].(map[string]interface{}); ok {
		if passed, ok := smartStatus["passed"].(bool); ok {
			attributes["smart_passed"] = boolToFloat(passed)
		}
	}

	return attributes
}

func smartMegaraid(dev, megaraidID string) map[string]float64 {
	output, _, err := runSmartctlCmd([]string{"-A", "-H", "-d", megaraidID, "--json=c", dev})
	if err != nil {
		log.Println("Error running smartctl for MegaRAID:", err)
		return nil
	}

	var result struct {
		Device struct {
			Protocol string `json:"protocol"`
		} `json:"device"`
		AtaSmartAttributes struct {
			Table []struct {
				ID    int    `json:"id"`
				Name  string `json:"name"`
				Value int    `json:"value"`
				Raw   struct {
					String string `json:"string"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		SmartStatus struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		log.Println("Error parsing MegaRAID JSON:", err)
		return nil
	}

	attributes := make(map[string]float64)
	if result.Device.Protocol == "ATA" {
		for _, attr := range result.AtaSmartAttributes.Table {
			name := attr.Name
			value := float64(attr.Value)
			rawValue := parseRawValue(attr.Raw.String)

			attributes[name] = value
			if rawValue != nil {
				attributes[name+"_raw"] = *rawValue
			}
		}
	} else if result.Device.Protocol == "SCSI" {
	}

	attributes["smart_passed"] = boolToFloat(result.SmartStatus.Passed)
	return attributes
}

func parseRawValue(rawStr string) *float64 {
	parts := strings.Fields(rawStr)
	if len(parts) == 0 {
		return nil
	}
	value, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return nil
	}
	return &value
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func sanitizeMetricName(name string) string {
	replacer := strings.NewReplacer(
		"-", "_",
		" ", "_",
		".", "",
		"/", "_",
	)
	return strings.ToLower(replacer.Replace(name))
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func main() {
	envAddress := os.Getenv("SMARTCTL_EXPORTER_ADDRESS")
	envPort := os.Getenv("SMARTCTL_EXPORTER_PORT")
	envIntervalStr := os.Getenv("SMARTCTL_REFRESH_INTERVAL")

	// Set comand line flags with pflag
	showVersion := pflag.Bool("version", false, "Show the version and exit")
	flagAddress := pflag.String("address", "", "Address to listen on")
	flagPort := pflag.String("port", "", "Port to listen on")
	flagInterval := pflag.Int("interval", 0, "Refresh interval in seconds")

	pflag.Parse()

	if *showVersion {
		fmt.Println("Version:", version)
		return
	}

	// Set defaults
	address := "0.0.0.0"
	if *flagAddress != "" {
		address = *flagAddress
	} else if envAddress != "" {
		address = envAddress
	}

	port := "9809"
	if *flagPort != "" {
		port = *flagPort
	} else if envPort != "" {
		port = envPort
	}

	refreshInterval := 60
	if *flagInterval != 0 {
		refreshInterval = *flagInterval
	} else if envIntervalStr != "" {
		if val, err := strconv.Atoi(envIntervalStr); err == nil {
			refreshInterval = val
		}
	}

	// Device inicialization
	devices = getDrives()

	// Run HTTP-server
	http.Handle("/metrics", promhttp.Handler())
	serverAddress := fmt.Sprintf("%s:%s", address, port)
	log.Printf("Server listening on http://%s/metrics", serverAddress)
	go func() {
		if err := http.ListenAndServe(serverAddress, nil); err != nil {
			log.Fatal(err)
		}
	}()

	// Starting a metrics collection cycle 
	ticker := time.NewTicker(time.Duration(refreshInterval) * time.Second)
	defer ticker.Stop()

	for {
		collect()
		<-ticker.C
	}
}
