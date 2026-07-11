#  OpsSweep
**The safe, simple way to clean up your cloud clutter.**[cite: 1]

### The Problem
Students, hobbyists, and solo developers routinely spin up AWS resources—like EC2 instances, EBS volumes, and load balancers—for coursework, side projects, or intense national-level competitions like HackToFuture. However, AWS free-tier limits are per-service and time-boxed[cite: 1]. An unattached Elastic IP, a forgotten snapshot, or an instance that outlived its 12-month window will silently bill your account with no warning until the invoice arrives[cite: 1]. 

Existing tools fail this demographic: they are either heavy, enterprise-grade FinOps platforms requiring a dedicated team, or scrappy, unsafe single-purpose scripts with no cost estimates[cite: 1]. 

### The Solution
OpsSweep is built specifically to answer two simple questions safely: *"what's costing me money right now?"* and *"can I get rid of it without breaking something I actually need?"*[cite: 1]

### Key Features
*   **Multi-Region Discovery Engine:** Uses the AWS SDK and concurrent goroutines to scan across ~17 AWS regions[cite: 1]. It enumerates EC2 instances, EBS volumes/snapshots, Elastic IPs, load balancers, RDS instances, and NAT gateways significantly faster than sequential scripts[cite: 1].
*   **Idle Heuristics Engine:** Replaces naive binary checks with a weighted confidence score[cite: 1]. It analyzes CloudWatch CPU/network metrics, structural signals (e.g., zero healthy targets on a load balancer), resource age, user-defined tags, and overall cost magnitude to accurately determine if a resource is truly abandoned[cite: 1].
*   **Cost Estimation & Reporting:** Maps idle resources to their expected monthly dollar cost using an embedded, static AWS pricing snapshot[cite: 1]. It generates a polished terminal UI table and a self-contained, highly shareable static HTML report[cite: 1].
*   **Safe Teardown:** The trust-critical feature that sets OpsSweep apart[cite: 1]. It defaults to a dry-run and requires explicit per-resource confirmation[cite: 1]. Before deleting, it takes backups (like EBS/RDS snapshots) and maintains a local audit journal of every action taken, allowing you to restore anything you might need later[cite: 1].
