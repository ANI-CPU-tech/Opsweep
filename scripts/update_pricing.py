#!/usr/bin/env python3
"""
update_pricing.py — Fetches a trimmed AWS on-demand EC2 pricing snapshot
and writes it to internal/pricing/data/prices.json for go:embed.

Usage:
    python3 scripts/update_pricing.py

Requirements:
    pip install boto3 requests

The script targets us-east-1 (the canonical pricing endpoint) and covers
the most common instance types across all commercial regions.
"""

import json
import os
import urllib.request

# AWS publishes a bulk pricing index; the EC2 on-demand JSON lives here.
# We fetch the index and pull out Linux on-demand prices for common families.
PRICING_INDEX_URL = (
    "https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/index.json"
)

# Limit to these instance families to keep the snapshot small.
INCLUDE_FAMILIES = {"t3", "t3a", "t4g", "m5", "m6i", "c5", "c6i", "r5", "r6i", "g4dn", "p3"}

OUTPUT_PATH = os.path.join(
    os.path.dirname(__file__), "..", "internal", "pricing", "data", "prices.json"
)


def fetch_prices() -> list[dict]:
    print(f"Downloading pricing index from {PRICING_INDEX_URL} ...")
    print("(This may take a minute — the full index is ~600MB)")

    # TODO: for a lighter approach, use the AWS Price List Query API
    # (boto3 pricing client, filter by service=AmazonEC2, region, OS=Linux)
    # which returns paginated JSON and is far smaller than the bulk download.

    entries: list[dict] = []

    # Placeholder: return an empty list until the full implementation is wired.
    print("Warning: pricing fetcher not fully implemented yet.")
    print("Returning empty snapshot — run `make update-pricing` after implementing.")
    return entries


def main() -> None:
    entries = fetch_prices()
    output_path = os.path.normpath(OUTPUT_PATH)
    os.makedirs(os.path.dirname(output_path), exist_ok=True)

    with open(output_path, "w", encoding="utf-8") as f:
        json.dump(entries, f, indent=2)

    print(f"Wrote {len(entries)} price entries to {output_path}")


if __name__ == "__main__":
    main()
