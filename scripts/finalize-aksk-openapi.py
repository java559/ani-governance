#!/usr/bin/env python3
"""Remove gnostic synthetic responses shadowed by explicit source annotations.

This is part of generation, never a manual edit of the generated document.
"""
import pathlib
import re

path = pathlib.Path(__file__).resolve().parents[1] / "app/admin/service/cmd/server/assets/openapi.yaml"

def remove_synthetic_200(text, route, status, method="post"):
    start = text.index("    " + route + ":\n")
    end = text.find("\n    /", start + 1)
    if end == -1:
        end = text.index("\ncomponents:", start)
    section = text[start:end]
    post = section.index("        " + method + ":\n")
    prefix, operation = section[:post], section[post:]
    if '"' + status + '":' not in operation and "'" + status + "':" not in operation:
        raise SystemExit("source Create operation must explicitly declare " + status)
    operation, count = re.subn(r"(?m)^                ['\"]?200['\"]?:\n(?:(?: {17,}.*|)\n)*", "", operation, count=1)
    if count != 1:
        raise SystemExit("expected one synthetic 200 response for " + route)
    return text[:start] + prefix + operation + text[end:]


def remove_shadowed_200(text):
    # gnostic prepends an inferred response before an explicit annotated 200.
    # Keep the source annotation without leaving duplicate YAML mapping keys.
    operations = r"(?m)^        (?:get|post|put|patch|delete|head|options):\n(?:(?: {9,}.*|)\n)*"
    response = r"(?m)^                ['\"]?200['\"]?:\n(?:(?: {17,}.*|)\n)*"

    def normalize(match):
        operation = match.group(0)
        candidates = list(re.finditer(response, operation))
        if len(candidates) > 2:
            raise SystemExit("ambiguous duplicate 200 responses")
        if len(candidates) == 2:
            inferred = candidates[0]
            return operation[:inferred.start()] + operation[inferred.end():]
        return operation

    return re.sub(operations, normalize, text)


def network_operation_schema(proto_source):
    """Derive the alias from the public DTO instead of cloning gnostic Operation.

    The public Operation currently contains only strings and timestamps. Fail
    closed on unsupported schema drift rather than silently losing new fields.
    Protobuf type names, field numbers and internal RPC messages stay intact.
    """
    source = re.sub(r"(?m)//[^\n]*", "", proto_source)
    matches = list(re.finditer(r"(?m)^message Operation\s*\{([^{}]*)\}", source))
    if len(matches) != 1:
        raise SystemExit("expected one public Network Operation DTO")
    fields = []
    names, numbers = set(), set()
    for statement in matches[0].group(1).split(";"):
        if not statement.strip():
            continue
        field = re.fullmatch(r'\s*(string|google\.protobuf\.Timestamp)\s+[A-Za-z_][A-Za-z0-9_]*\s*=\s*([1-9][0-9]*)\s*\[\s*json_name\s*=\s*"([A-Za-z_][A-Za-z0-9_]*)"\s*\]\s*', statement)
        if field is None:
            raise SystemExit("unsupported public Network Operation schema field")
        kind, number, name = field.groups()
        if name in names or number in numbers:
            raise SystemExit("duplicate public Network Operation field")
        names.add(name)
        numbers.add(number)
        fields.append((name, kind))
    if not fields:
        raise SystemExit("public Network Operation schema is empty")
    result = "        NetworkOperation:\n            type: object\n            properties:\n"
    for name, kind in fields:
        result += "                " + name + ":\n                    type: string\n"
        if kind == "google.protobuf.Timestamp":
            result += "                    format: date-time\n"
    return result


def repair_network_operation(text, proto_source=None):
    """Scope the Operation name collision repair to the five Network DTOs."""
    responses = ("GetOperationResponse", "GetLoadBalancerOperationResponse", "CreateLoadBalancerResponse", "UpdateLoadBalancerResponse", "DeleteLoadBalancerResponse")
    if "#/components/schemas/NetworkOperation" not in text:
        if any("        " + name + ":\n" in text for name in responses):
            raise SystemExit("Network operation routes must explicitly reference NetworkOperation")
        return text
    if re.search(r"(?m)^        NetworkOperation:\n", text):
        raise SystemExit("NetworkOperation schema must be derived once during generation")
    if proto_source is None:
        source = pathlib.Path(__file__).resolve().parents[1] / "api/protos/catalog/service/v1/vpc.proto"
        proto_source = source.read_text()
    for name in responses:
        component = re.compile(r"(?m)^        " + name + r":\n(?:(?: {9,}.*|)\n)*")
        sections = list(component.finditer(text))
        if len(sections) > 1:
            raise SystemExit("duplicate Network response component " + name)
        if not sections:
            continue  # Accepted subset generator fixtures may omit an RPC.
        section = sections[0].group(0)
        section, count = re.subn(r"(?m)(^                operation:\n                    \$ref: ['\"]#/components/schemas/)Operation(['\"]\n)", r"\g<1>NetworkOperation\2", section)
        if count != 1:
            raise SystemExit("expected one Network operation reference in " + name)
        text = text[:sections[0].start()] + section + text[sections[0].end():]
    marker = "components:\n    schemas:\n"
    if text.count(marker) != 1:
        raise SystemExit("expected one OpenAPI schema component section")
    return text.replace(marker, marker + network_operation_schema(proto_source), 1)


def add_instance_log_stream(text):
    # A protobuf server stream has a dedicated HTTP implementation. Describe
    # its actual SSE wire format instead of gnostic's inferred unary JSON.
    if "            operationId: InstanceService_GetInstanceRuntime\n" not in text:
        return text
    route = "    /api/v1/instances/{instance_id}/logs:\n"
    if route in text:
        raise SystemExit("instance logs must be declared once as SSE")
    operation = """    /api/v1/instances/{instance_id}/logs:
        get:
            tags:
                - InstanceService
            operationId: InstanceService_StreamInstanceLogs
            description: Read current workspace stdout/stderr. READY is verified before HTTP headers; subsequent errors are SSE events. No resume cursor.
            security:
                - BearerAuth: []
            parameters:
                - name: instance_id
                  in: path
                  required: true
                  schema:
                    type: string
                    format: uuid
                - name: tailLines
                  in: query
                  schema:
                    type: integer
                    minimum: 1
                    maximum: 1000
                    default: 100
                - name: follow
                  in: query
                  schema:
                    type: boolean
                    default: true
            responses:
                "200":
                    description: SSE log/end/error JSON events with comment heartbeats; no-store and proxy buffering disabled.
                    content:
                        text/event-stream:
                            schema:
                                type: string
                "400":
                    description: Invalid query
                "403":
                    description: Instance access denied
                "404":
                    description: Instance or current workspace Pod absent
                "429":
                    description: Connection limit reached
                "503":
                    description: Diagnostic target or upstream unavailable before READY
"""
    marker = "components:\n"
    if text.count(marker) != 1:
        raise SystemExit("expected one OpenAPI components section")
    return text.replace(marker, operation + marker, 1)


def finalize(text):
    text = remove_synthetic_200(text, "/api/v1/auth/api-keys", "201")
    # Minimal generator fixtures may omit ModelDev. Its public contract test
    # separately requires this path in the complete service document.
    if "    /api/v1/modeldev/executions:\n" in text:
        text = remove_synthetic_200(text, "/api/v1/modeldev/executions", "202")
        text = remove_synthetic_200(text, "/api/v1/modeldev/executions/{execution_id}:stop", "202")
    for route, status, method in (
        ("/api/v1/admin/compute-presets", "201", "post"),
        ("/api/v1/admin/compute-presets/{id}", "204", "delete"),
        ("/api/v1/instances", "202", "post"),
        ("/api/v1/instances/{data.instance_id}:start", "202", "post"),
        ("/api/v1/instances/{data.instance_id}:stop", "202", "post"),
        ("/api/v1/instances/{data.instance_id}", "202", "delete"),
        ("/api/v1/instances/{data.instance_id}/exec", "202", "post"),
    ):
        if "    " + route + ":\n" in text:
            text = remove_synthetic_200(text, route, status, method)
    # response_body endpoints retain their explicit public-object schema,
    # including the Network/Image mutation responses and existing details.
    return add_instance_log_stream(repair_network_operation(remove_shadowed_200(text)))


if __name__ == "__main__":
    path.write_text(finalize(path.read_text()))
