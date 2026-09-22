#!/usr/bin/env python3
"""Generate a compact Ignition REST operation catalog from /openapi.json."""

from __future__ import print_function

import argparse
import hashlib
import json
import os
import sys

HTTP_METHODS = ("delete", "get", "head", "options", "patch", "post", "put", "trace")

ROUTE_MODULES = (
    ("/data/alarm-notification/", "module", "com.inductiveautomation.alarm-notification"),
    ("/data/eam/", "module", "com.inductiveautomation.eam"),
    ("/data/event-stream/", "module", "com.inductiveautomation.eventstream"),
    ("/data/fsql/", "module", "com.inductiveautomation.sqlbridge"),
    ("/data/opc-ua/", "module", "com.inductiveautomation.opcua"),
    ("/data/perspective/", "module", "com.inductiveautomation.perspective"),
    ("/data/reporting/", "module", "com.inductiveautomation.reporting"),
    ("/data/sfc/", "module", "com.inductiveautomation.sfc"),
    ("/data/vision/", "module", "com.inductiveautomation.vision"),
    ("/data/mcp/", "private-module", "com.inductiveautomation.mcp"),
)

RESOURCE_MODULE_ALIASES = {
    "com.inductiveautomation.mcp": ("private-module", "com.inductiveautomation.mcp"),
    "com.inductiveautomation.opcua.drivers.bacnet": (
        "module",
        "com.inductiveautomation.opcua.drivers.bacnet,com.inductiveautomation.opcua",
    ),
    "com.inductiveautomation.sip-notification": (
        "module",
        "com.inductiveautomation.phone-notification",
    ),
}


def classify_path(path):
    for prefix, kind, modules in ROUTE_MODULES:
        if path.startswith(prefix):
            return kind, modules

    resources_prefix = "/data/api/v1/resources/"
    if path.startswith(resources_prefix):
        segments = path[len(resources_prefix):].split("/")
        module_segment = next(
            (segment for segment in segments if segment.startswith("com.")),
            None,
        )
        if module_segment:
            return RESOURCE_MODULE_ALIASES.get(
                module_segment,
                ("module", module_segment),
            )

    return "platform", "-"


def generate(openapi_path, ignition_version):
    with open(openapi_path, "rb") as handle:
        raw = handle.read()

    document = json.loads(raw.decode("utf-8"))
    paths = document.get("paths")
    if not isinstance(paths, dict):
        raise ValueError(
            "OpenAPI document does not contain an object-valued 'paths' member"
        )

    rows = []
    for path, path_item in paths.items():
        if not isinstance(path_item, dict):
            continue
        for method in HTTP_METHODS:
            if method not in path_item:
                continue
            kind, modules = classify_path(path)
            rows.append((method.upper(), path, kind, modules))

    rows.sort(key=lambda row: (row[1], row[0]))
    fingerprint = hashlib.sha256(raw).hexdigest()
    lines = [
        "# Ignition %s public REST operation catalog generated from a real Gateway /openapi.json."
        % ignition_version,
        "# source_sha256=%s" % fingerprint,
        "# method<TAB>path_template<TAB>owner_kind(platform|module|private-module)<TAB>required_module_ids(comma-separated or -)",
    ]
    lines.extend("\t".join(row) for row in rows)
    return "\n".join(lines) + "\n", len(rows)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("openapi", help="Ignition /openapi.json file")
    parser.add_argument(
        "--ignition-version",
        required=True,
        help="Ignition version represented by the OpenAPI snapshot",
    )
    parser.add_argument("--output", required=True, help="Catalog output path")
    args = parser.parse_args(argv)

    content, operation_count = generate(args.openapi, args.ignition_version)
    output_dir = os.path.dirname(os.path.abspath(args.output))
    if output_dir and not os.path.isdir(output_dir):
        os.makedirs(output_dir)

    with open(args.output, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(content)

    print("Generated %d REST operations -> %s" % (operation_count, args.output))
    return 0


if __name__ == "__main__":
    sys.exit(main())
