# smartctl_exporter

`smartctl_exporter` is a Go-based exporter that polls the smartmontools service to collect S.M.A.R.T. metrics from hard drives and exposes them for Prometheus monitoring.

## Features

- Collects S.M.A.R.T. metrics from all connected hard drives.
- Exposes metrics in a Prometheus-compatible format.
- Customizable listening address and port.
- Configurable refresh interval for polling smartmontools.

## Installation

1. **Prerequisites**: Ensure you have [Go](https://golang.org/dl/) installed.
2. **Clone the repository**:

   ```bash
   git clone https://github.com/yourusername/smartctl_exporter.git
   ```
3. **Build the exporter**:

   ```bash
   cd smartctl_exporter
   go build
   ```

## Usage

Run the exporter with default settings:

```bash
./smartctl_exporter
```

### Command-Line Flags

```plaintext
--address string   Address to listen on (default "0.0.0.0")
--port string      Port to listen on (default "9000")
--interval int     Refresh interval in seconds (default 60)
--version          Show the version and exit
```

### Examples

- **Specify a custom address and port**:

  ```bash
  ./smartctl_exporter --address 127.0.0.1 --port 8080
  ```

- **Set a custom refresh interval**:

  ```bash
  ./smartctl_exporter --interval 120
  ```

- **Display version information**:

  ```bash
  ./smartctl_exporter --version
  ```

## Prometheus Configuration

Add the following to your `prometheus.yml` file:

```yaml
scrape_configs:
  - job_name: 'smartctl_exporter'
    static_configs:
      - targets: ['localhost:9809']  # Update if using a custom address or port
```

## Metrics Exposed

The exporter provides the following metrics (examples):

- `smartctl_temperature_celsius`
- `smartctl_power_on_hours`
- `smartctl_reallocated_sector_count`

These metrics include labels such as `device` and `model`.

## Contributing

Contributions are welcome! Please open an issue or submit a pull request.

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.

## Acknowledgements

- [smartmontools](https://www.smartmontools.org/) for providing the tools to access S.M.A.R.T. data.
- [Prometheus](https://prometheus.io/) for the monitoring ecosystem.
