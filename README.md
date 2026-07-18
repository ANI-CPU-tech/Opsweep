# OpsSweep

### An Interactive AWS FinOps Shell and Automated Remediation Engine

> **Stop paying for infrastructure you forgot existed.**

OpsSweep is a developer-first CLI tool that scans your AWS account for idle and wasted resources, quantifies the cost, and gives you the power to eliminate that waste — safely and interactively — from a single terminal session.

---

## The Problem

Every AWS account accumulates invisible cost: unattached EBS volumes accruing storage charges with no instance to serve, Elastic IPs billed at $0.005/hr for being allocated but unused, NAT Gateways charging a fixed ~$32/month regardless of whether a single packet flows through them, and idle RDS databases running at full instance-hour rates with zero connections.

These are not edge cases. They are the predictable residue of fast-moving engineering teams: a terminated instance leaves its volume behind, a prototype environment is torn down but the EIP is never released, a cost-review task never makes it off the backlog.

Traditional solutions fall short in two ways:

- **One-shot scripts** find waste once but leave you to manually cross-reference, verify, and act — with no safety net.
- **Dashboard tools** surface the problem visually but live outside your terminal and offer no integrated remediation path.

OpsSweep closes the loop: discover, quantify, and remediate in a single stateful session, with confidence scoring and dry-run protection baked in at every step.

---

## The Solution

### Key Features

- **Interactive REPL Shell** — A persistent, stateful session built on [`go-prompt`](https://github.com/c-bata/go-prompt), providing real-time autocomplete dropdowns as you type. The terminal UI dynamically adapts to any terminal width, rendering a professional two-column welcome box on startup.

- **Concurrent Multi-Region Discovery** — Scans every enabled AWS region in parallel using Go's `errgroup`-based two-level fan-out: one goroutine per region, one goroutine per resource type within each region. A full account scan completes in seconds, not minutes.

- **Heuristics Engine** — Goes beyond simple state checks. Fetches 14-day CloudWatch metrics to detect zombie EC2 instances (CPU < 2%), idle NAT Gateways (zero active connections), and dormant RDS databases (zero average connections). Combines structural signals, CloudWatch utilization, resource age, and tag analysis into a single weighted confidence score (0.0–1.0).

- **Safety-First Remediation** — All scans are read-only by default. The remediation engine requires an explicit `--teardown` flag and prints a live warning before making any mutating AWS API call. Protected resources (tagged `keep=true` or `env=prod`) are permanently excluded, regardless of their score.

- **Financial Auditing** — Generates a self-contained, Tailwind CSS-styled HTML report (`audit.html`) with a monthly waste summary, per-resource cost estimates, confidence scores, and the heuristic reasons behind each finding. No external dependencies — the report is a single portable file.

---

## Supported Resources

| Resource | Idle Signal | Est. Monthly Waste |
|---|---|---|
| **EBS Volume** | State = `available` (unattached) | ~$4.00 |
| **Elastic IP** | No associated instance or ENI | ~$3.60 |
| **EC2 Instance** | Stopped, or running with CPU < 2% | ~$0–$30.00 |
| **NAT Gateway** | Zero active connections over 14 days | ~$32.40 |
| **RDS Database** | Zero average connections over 14 days | ~$14.60 |

---

## Getting Started

### Prerequisites

- Go 1.21 or later
- AWS credentials configured (`~/.aws/credentials` or environment variables)
- IAM permissions for: `ec2:Describe*`, `rds:Describe*`, `cloudwatch:GetMetricStatistics`

### Installation

```bash
# 1. Clone the repository
git clone https://github.com/anirudh/opssweep.git
cd opssweep

# 2. Install dependencies
go mod tidy

# 3. Build the binary
go build -o opsweep ./cmd/opssweep

# 4. Run it
./opsweep
```

---

## Usage

### Launching the Shell

```bash
# Using your default AWS profile
./opsweep

# Against a specific profile or region
AWS_PROFILE=staging AWS_DEFAULT_REGION=us-east-1 ./opsweep

# Against a local mock (e.g. LocalStack) for development
AWS_ENDPOINT_URL=http://localhost:4566 go run cmd/opssweep/main.go
```

On launch, OpsSweep prints a welcome banner and drops you into the interactive shell

### Shell Commands

| Command | Description |
|---|---|
| `/scan` | Scan all enabled AWS regions and print a waste report table. **Read-only — no changes are made.** |
| `/scan --teardown` | Scan and immediately execute **live, permanent deletion** of all resources with a confidence score ≥ 0.90. Prints a red warning before proceeding. |
| `/report` | Run a scan and write a Tailwind-styled HTML audit report to `audit.html` in the current directory. |
| `/report --output=<path>` | Same as `/report` but write to a custom file path. |
| `/help` | Print the full command reference. |
| `/clear` | Clear the terminal and redraw the welcome banner. |
| `/exit` | End the session. |

### Example Session

```
> /scan

[SYSTEM] Scanning AWS regions for idle resources. This may take a moment...

RESOURCE ID              TYPE              REGION       STATE       CONFIDENCE   MONTHLY WASTE
eipalloc-0a1b2c3d       ec2:elastic-ip    us-east-1    unattached  100%         $3.60
vol-0abc123def456789    ec2:ebs-volume    us-west-2    available   90%          $4.00
nat-0deadbeef12345678   ec2:nat-gateway   eu-west-1    available   95%          $32.40
------------------------------------------------------------------------
TOTAL POTENTIAL SAVINGS: $40.00/mo

> /report

[REPORT] Successfully generated FinOps audit at audit.html

> /scan --teardown

[WARNING] Executing live teardown. Resources will be permanently deleted.

[TEARDOWN] Successfully released Elastic IP eipalloc-0a1b2c3d in us-east-1
[TEARDOWN] Successfully deleted EBS volume vol-0abc123def456789 in us-west-2
[TEARDOWN] Successfully deleted NAT Gateway nat-0deadbeef12345678 in eu-west-1
```

---

## Architecture Overview

OpsSweep is built around a clean separation of concerns across focused, independently testable packages:

```
cmd/opssweep/          — Entry point; loads AWS config, starts the REPL
internal/
  cli/                 — Interactive shell (go-prompt executor + completer)
  discovery/           — AWS API adapters (scanner, runner, per-resource files)
  heuristics/          — Confidence scoring engine (tags, state, CloudWatch, age)
  pricing/             — Static waste estimates + embedded pricing catalog
  remediation/         — Mutating API calls (EBS, EIP, NAT Gateway, RDS)
  report/              — HTML report generation (html/template + go:embed)
  ui/                  — Terminal rendering (banner, waste table)
```

**Key technical decisions:**

- **AWS SDK for Go V2** — Used throughout. Per-region clients are constructed on demand via `cfg.Copy()`, enabling safe concurrent use across goroutines without shared mutable state.
- **`errgroup`-based concurrency** — Two-level fan-out (regions → resource types) maximises throughput while propagating the first error cleanly and respecting context cancellation (Ctrl+C stops an in-flight scan immediately).
- **Pointer semantics for nullable metrics** — CloudWatch fields (`CPUUtilizationPercent`, `ConnectionCount`, `DatabaseConnections`) use `*float64` to distinguish "data was fetched and is zero" from "data was not fetched" — a distinction that matters for the heuristics engine.
- **`go:embed` for static assets** — The HTML report template and pricing JSON are embedded into the binary at compile time; the shipped binary has zero runtime file dependencies.
- **`go-prompt` for the REPL** — Provides raw-mode terminal input with real-time prefix-filtered autocomplete. The executor and completer are decoupled from all AWS logic, keeping the shell layer thin.

---

## License

MIT — see [LICENSE](LICENSE) for details.
