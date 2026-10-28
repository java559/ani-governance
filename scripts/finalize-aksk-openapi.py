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
    # 行首锚定匹配顶层 components:（不能用裸子串：路由 /api/v1/platform/components
    # 的路径行同样以 "components:\n" 结尾，会被误计入）。
    marker = "\ncomponents:\n"
    if text.count(marker) != 1:
        raise SystemExit("expected one OpenAPI components section")
    return text.replace(marker, "\n" + operation + "components:\n", 1)


def add_execution_log_stream(text):
    # The training log channel is a hand-written SSE handler, not a protobuf
    # annotation, so the generator cannot infer it. The tenant gate still looks
    # the route up by template, which means the contract has to be declared here.
    if "            operationId: ModelDevService_ListExecutionEvents\n" not in text:
        return text
    route = "    /api/v1/modeldev/executions/{execution_id}/logs/stream:\n"
    if route in text:
        raise SystemExit("execution log stream must be declared once as SSE")
    operation = """    /api/v1/modeldev/executions/{execution_id}/logs/stream:
        get:
            tags:
                - ModelDevService
            operationId: ModelDevService_StreamExecutionLogs
            description: Follow one container of one execution. READY is verified before HTTP headers; later failures arrive as control frames. Reconnect with the server-issued sinceBytes offset.
            security:
                - BearerAuth: []
            parameters:
                - name: execution_id
                  in: path
                  required: true
                  schema:
                    type: string
                    format: uuid
                - name: podUid
                  in: query
                  schema:
                    type: string
                    format: uuid
                - name: container
                  in: query
                  schema:
                    type: string
                - name: previous
                  in: query
                  schema:
                    type: boolean
                    default: false
                - name: sinceBytes
                  in: query
                  schema:
                    type: integer
                    format: int64
                    minimum: 0
                    maximum: 1073741824
                    default: 0
            responses:
                "200":
                    description: SSE READY/DATA/CLOSED/REJECTED frames with comment heartbeats; no-store and proxy buffering disabled.
                    content:
                        text/event-stream:
                            schema:
                                type: string
                "400":
                    description: Invalid query
                "403":
                    description: Execution access denied
                "404":
                    description: Execution or selected Pod absent
                "429":
                    description: Connection limit reached
                "503":
                    description: Upstream frame stream unavailable before READY
"""
    marker = "\ncomponents:\n"
    if text.count(marker) != 1:
        raise SystemExit("expected one OpenAPI components section")
    return text.replace(marker, "\n" + operation + "components:\n", 1)


def add_artifact_download(text):
    # The byte relay is a hand-written handler next to the URL exchange, so the
    # generator cannot infer it. The tenant gate looks the route up by template,
    # which means the contract has to be declared here or the route answers 403.
    if "            operationId: ModelDevService_AuthorizeArtifactDownload\n" not in text:
        return text
    route = "    /api/v1/modeldev/artifacts/{artifact_id}/download:\n"
    if route in text:
        raise SystemExit("artifact download must be declared once as a byte stream")
    operation = """    /api/v1/modeldev/artifacts/{artifact_id}/download:
        get:
            tags:
                - ModelDevService
            operationId: ModelDevService_DownloadArtifact
            description: Relay the artifact bytes. The URL exchange returns a signed URL on the object store's in-cluster host, which a browser cannot reach; this endpoint fetches that object server-side and copies it out. Accepts no query.
            security:
                - BearerAuth: []
            parameters:
                - name: artifact_id
                  in: path
                  required: true
                  schema:
                    type: string
                    format: uuid
            responses:
                "200":
                    description: Artifact bytes as an attachment. The type collapses to application/octet-stream unless it is a known safe textual type.
                    content:
                        application/octet-stream:
                            schema:
                                type: string
                                format: binary
                "400":
                    description: Invalid UUID or a query was supplied
                "403":
                    description: Artifact access denied
                "404":
                    description: Artifact absent or not visible
                "412":
                    description: Artifact not published
                "502":
                    description: Object store unreachable or returned an error
                "504":
                    description: Object store timed out
"""
    marker = "\ncomponents:\n"
    if text.count(marker) != 1:
        raise SystemExit("expected one OpenAPI components section")
    return text.replace(marker, "\n" + operation + "components:\n", 1)


def add_platform_component_log_stream(text):
    # The component log channel is a hand-written SSE handler next to the
    # component status snapshot, so the generator cannot infer it. The route is
    # looked up by template, which means the contract must be declared here.
    if "            operationId: PlatformComponentService_ListPlatformComponents\n" not in text:
        return text
    route = "    /api/v1/platform/components/{component}/logs/stream:\n"
    if route in text:
        raise SystemExit("platform component log stream must be declared once as SSE")
    operation = """    /api/v1/platform/components/{component}/logs/stream:
        get:
            tags:
                - PlatformComponentService
            operationId: PlatformComponentService_StreamComponentLogs
            description: Read the six-field log stream of one registered platform component as SSE. Platform identity and component:log:read are verified before HTTP headers; the stream closes normally after 8 seconds and the client reconnects. No cross-connection cursor.
            security:
                - BearerAuth: []
            parameters:
                - name: component
                  in: path
                  required: true
                  schema:
                    type: string
                    description: Must be a name from GET /api/v1/platform/components. Arbitrary pod, namespace or index names are rejected.
                - name: limit
                  in: query
                  schema:
                    type: integer
                    minimum: 1
                    maximum: 200
                    default: 100
                - name: interval_seconds
                  in: query
                  schema:
                    type: integer
                    minimum: 1
                    maximum: 5
                    default: 2
            responses:
                "200":
                    description: SSE comment heartbeats plus event log frames carrying the six string fields; no-store and proxy buffering disabled. Closing after 8 seconds is normal.
                    content:
                        text/event-stream:
                            schema:
                                type: string
                "400":
                    description: Invalid query, duplicated parameter or unsupported Last-Event-ID
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/PlatformComponentLogError'
                "403":
                    description: Platform identity or component - log read capability missing
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/PlatformComponentLogError'
                "404":
                    description: Component is not registered
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/PlatformComponentLogError'
                "429":
                    description: Concurrent connection limit reached; honour Retry-After
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/PlatformComponentLogError'
                "503":
                    description: Log backend not configured or unavailable
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/PlatformComponentLogError'
                "504":
                    description: Log query timed out
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/PlatformComponentLogError'
"""
    # 行首锚定匹配顶层 components:（不能用裸子串：路由 /api/v1/platform/components
    # 的路径行同样以 "components:\n" 结尾，会被误计入）。
    marker = "\ncomponents:\n"
    if text.count(marker) != 1:
        raise SystemExit("expected one OpenAPI components section")
    return text.replace(marker, "\n" + operation + "components:\n", 1)


def add_platform_component_log_error_schema(text):
    # 该入口沿用旧版字符串 code 错误体；schema 单独声明一次，供上面 6 个状态码引用。
    if "            operationId: PlatformComponentService_StreamComponentLogs\n" not in text:
        return text
    if "        PlatformComponentLogError:\n" in text:
        raise SystemExit("platform component log error schema declared twice")
    schema = """        PlatformComponentLogError:
            type: object
            required:
                - code
                - message
                - request_id
            properties:
                code:
                    type: string
                    description: Legacy string code, such as COMPONENT_DIAGNOSTICS_FAILED. Other Gov routes use a numeric code.
                message:
                    type: string
                request_id:
                    type: string
"""
    marker = "    schemas:\n"
    if text.count(marker) != 1:
        raise SystemExit("expected one OpenAPI schemas section")
    return text.replace(marker, marker + schema, 1)


def require_compute_usage(text, proto_source=None):
    # gnostic v0.7.1 does not carry field behavior/property enums into GET
    # query parameters. Keep the emitted contract tied to its Proto input.
    marker = "    /api/v1/compute-presets:\n"
    if marker not in text:
        return text
    if text.count(marker) != 1:
        raise SystemExit("expected one compute catalog route")
    if proto_source is None:
        proto_source = (pathlib.Path(__file__).resolve().parents[1] / "api/protos/compute/service/v1/compute.proto").read_text()
    field = re.search(r"string usage = 7 \[(.*?)\];", proto_source, re.S)
    if field is None or "(google.api.field_behavior) = REQUIRED" not in field.group(1):
        raise SystemExit("compute usage must be required in Proto")
    values = re.findall(r'yaml:\s*"([A-Z_]+)"', field.group(1))
    if len(values) != 4 or len(set(values)) != 4:
        raise SystemExit("compute usage enum missing or ambiguous")
    start = text.index(marker)
    end = text.find("\n    /", start + len(marker))
    if end < 0:
        end = text.index("\ncomponents:", start)
    section = text[start:end]
    pattern = r"(                - name: usage\n                  in: query\n)(.*?)(?=                - name:|            responses:)"
    def annotate(match):
        body = match.group(2)
        if "required:" in body or "enum:" in body:
            raise SystemExit("compute usage query was already annotated")
        body = body.replace("                    type: string\n", "                    type: string\n                    enum:\n" + "".join("                        - " + v + "\n" for v in values), 1)
        return match.group(1) + "                  required: true\n" + body
    section, count = re.subn(pattern, annotate, section, flags=re.S)
    if count != 1:
        raise SystemExit("expected one compute usage query")
    return text[:start] + section + text[end:]


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
    return add_platform_component_log_error_schema(add_platform_component_log_stream(add_artifact_download(add_execution_log_stream(add_instance_log_stream(require_compute_usage(repair_network_operation(remove_shadowed_200(text))))))))


if __name__ == "__main__":
    path.write_text(finalize(path.read_text()))
