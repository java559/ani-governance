"""Offline response-key regression tests; execute on the authorized remote."""
import importlib.util
from pathlib import Path
import re
import unittest

spec = importlib.util.spec_from_file_location('finalize_aksk_openapi', Path(__file__).resolve().parents[1] / 'finalize-aksk-openapi.py')
finalizer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(finalizer)


class FinalizeOpenAPITests(unittest.TestCase):
    def test_direct_object_schema_wins_for_every_http_method(self):
        for method in ['get', 'post', 'patch', 'delete']:
            with self.subTest(method=method):
                text = f'''    /api/v1/networks/resources:
        {method}:
            responses:
                "200":
                    description: inferred wrapper
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/CreateVPCResponse'
                "200":
                    description: public object
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/VPC'
                "400":
                    description: bad request
components:
'''
                normalized = finalizer.remove_shadowed_200(text)
                self.assertEqual(normalized.count('"200":'), 1)
                self.assertIn("#/components/schemas/VPC'", normalized)
                self.assertNotIn('CreateVPCResponse', normalized)
                self.assertIn('"400":', normalized)

    def test_combination_response_is_preserved(self):
        text = '''    /api/v1/networks/load-balancers:
        post:
            responses:
                "200":
                    description: complete resource plus operation
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/CreateLoadBalancerResponse'
components:
'''
        self.assertEqual(finalizer.remove_shadowed_200(text), text)

    def test_three_duplicate_responses_fail_closed(self):
        text = '        post:\n            responses:\n' + ('                "200":\n                    description: ambiguous\n' * 3)
        with self.assertRaises(SystemExit):
            finalizer.remove_shadowed_200(text)

    def test_network_operation_collision_is_scoped_and_source_derived(self):
        text = '''paths:
    /api/v1/networks/operations/{operation_id}:
        get:
            responses:
                "200":
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/NetworkOperation'
components:
    schemas:
        Operation:
            type: object
            properties:
                tags:
                    type: array
                    items:
                        type: string
        UnrelatedResponse:
            type: object
            properties:
                operation:
                    $ref: '#/components/schemas/Operation'
'''
        responses = ['GetOperationResponse', 'GetLoadBalancerOperationResponse', 'CreateLoadBalancerResponse', 'UpdateLoadBalancerResponse', 'DeleteLoadBalancerResponse']
        for response in responses:
            text += f'''        {response}:
            type: object
            properties:
                operation:
                    $ref: '#/components/schemas/Operation'
'''
        proto_source = (Path(__file__).resolve().parents[2] / 'api/protos/catalog/service/v1/vpc.proto').read_text()
        normalized = finalizer.repair_network_operation(text, proto_source)
        self.assertEqual(normalized.count('        NetworkOperation:\n'), 1)
        self.assertEqual(normalized.count("$ref: '#/components/schemas/NetworkOperation'"), 6)
        self.assertIn("        UnrelatedResponse:\n            type: object\n            properties:\n                operation:\n                    $ref: '#/components/schemas/Operation'", normalized)
        self.assertIn('        Operation:\n            type: object\n            properties:\n                tags:\n                    type: array\n', normalized)
        alias = normalized.split('        NetworkOperation:\n', 1)[1].split('        Operation:\n', 1)[0]
        fields = re.findall(r'^                ([A-Za-z_][A-Za-z0-9_]*):$', alias, re.MULTILINE)
        self.assertEqual(fields, ['id', 'resource_id', 'resource_type', 'kind', 'state', 'reason', 'created_at', 'updated_at', 'completed_at', 'next_attempt_at', 'reason_message'])
        self.assertEqual(alias.count('format: date-time'), 4)
        self.assertEqual(alias.count('type: string'), 11)
        self.assertNotIn('tags:', alias)

    def test_network_schema_uses_explicit_json_names_and_rejects_schema_drift(self):
        source = '''message Operation {
 string resource_id = 1 [json_name = "resourceID"];
 google.protobuf.Timestamp created_at = 2 [json_name = "createdAt"];
}'''
        schema = finalizer.network_operation_schema(source)
        self.assertIn('                resourceID:\n                    type: string\n', schema)
        self.assertIn('                createdAt:\n                    type: string\n                    format: date-time\n', schema)
        self.assertNotIn('resource_id:', schema)
        for invalid in [source.replace('string resource_id', 'int64 resource_id'), source.replace('"createdAt"', '"resourceID"'), 'message Other {}']:
            with self.subTest(source=invalid):
                with self.assertRaises(SystemExit):
                    finalizer.network_operation_schema(invalid)

    def test_network_schema_refuses_old_direct_annotation(self):
        text = '''components:
    schemas:
        CreateLoadBalancerResponse:
            type: object
            properties:
                operation:
                    $ref: '#/components/schemas/Operation'
'''
        with self.assertRaises(SystemExit):
            finalizer.repair_network_operation(text)

    def test_instance_operation_schema_is_source_derived_and_scoped(self):
        text = """paths:
    /api/v1/instance-operations/{operation_id}:
        get:
            operationId: InstanceService_GetInstanceOperation
            responses:
                "200":
                    content:
                        application/json:
                            schema:
                                $ref: '#/components/schemas/Operation'
components:
    schemas:
        Operation:
            type: object
            properties:
                tags:
                    type: array
        Unrelated:
            properties:
                operation:
                    $ref: '#/components/schemas/Operation'
"""
        source = (Path(__file__).resolve().parents[2] / 'api/protos/instance/service/v1/instance.proto').read_text()
        result = finalizer.repair_instance_operation(text, source)
        self.assertEqual(result.count("$ref: '#/components/schemas/InstanceOperation'"), 1)
        self.assertIn("$ref: '#/components/schemas/Operation'", result)
        schema = result.split('        InstanceOperation:\n', 1)[1].split('        Operation:\n', 1)[0]
        fields = re.findall(r'^                ([A-Za-z_][A-Za-z0-9_]*):$', schema, re.MULTILINE)
        self.assertEqual(fields, ['id', 'instanceId', 'action', 'intentVersion', 'state', 'reason', 'phase', 'target_configuration', 'final_run_state', 'applied_mount_version', 'completion_scope', 'failure_reason'])
        self.assertIn('nullable: true', schema)
        self.assertNotIn('tags:', schema)
        with self.assertRaises(SystemExit):
            finalizer.repair_instance_operation(result, source)
        with self.assertRaises(SystemExit):
            finalizer.instance_operation_schema(source.replace('int64 intent_version = 4', 'double intent_version = 4'))

    def test_compute_usage_is_required_and_enum_comes_from_proto(self):
        text = """    /api/v1/compute-presets:
        get:
            parameters:
                - name: usage
                  in: query
                  schema:
                    type: string
            responses:
                "200": {}
components:
"""
        result = finalizer.require_compute_usage(text)
        self.assertIn("required: true", result)
        for usage in ["SANDBOX", "CONTAINER_INSTANCE", "TRAINING", "INFERENCE", "VIRTUAL_MACHINE"]:
            self.assertIn("- " + usage, result)
        with self.assertRaises(SystemExit):
            finalizer.require_compute_usage(text, 'string usage = 7;')
        for enum in ['', 'enum: [{yaml: "SANDBOX"}, {yaml: "SANDBOX"}]']:
            with self.assertRaises(SystemExit):
                finalizer.require_compute_usage(text, 'string usage = 7 [(google.api.field_behavior) = REQUIRED, ' + enum + '];')

    def test_platform_component_log_stream_is_declared_once_as_sse(self):
        text = """paths:
    /api/v1/platform/components:
        get:
            operationId: PlatformComponentService_ListPlatformComponents
            responses:
                "200": {}
components:
    schemas:
        Existing:
            type: object
"""
        result = finalizer.add_platform_component_log_stream(text)
        self.assertEqual(result.count("    /api/v1/platform/components/{component}/logs/stream:\n"), 1)
        self.assertIn("            operationId: PlatformComponentService_StreamComponentLogs\n", result)
        self.assertIn("                        text/event-stream:\n", result)
        for code in ['"400"', '"403"', '"404"', '"429"', '"503"', '"504"']:
            self.assertIn("                " + code + ":\n", result)
        self.assertIn("                - name: limit\n", result)
        self.assertIn("                    maximum: 200\n", result)
        self.assertIn("                - name: interval_seconds\n", result)
        self.assertIn("                    maximum: 5\n", result)
        # 状态快照路由必须原样保留。
        self.assertIn("    /api/v1/platform/components:\n", result)
        # 二次应用必须 fail closed，不能悄悄写两份。
        with self.assertRaises(SystemExit):
            finalizer.add_platform_component_log_stream(result)

    def test_platform_component_log_stream_skips_absent_service(self):
        text = """paths:
    /api/v1/health:
        get:
            responses:
                "200": {}
components:
    schemas:
        Existing:
            type: object
"""
        self.assertEqual(finalizer.add_platform_component_log_stream(text), text)
        self.assertEqual(finalizer.add_platform_component_log_error_schema(text), text)

    def test_platform_component_log_error_schema_keeps_string_code(self):
        text = """paths:
    /api/v1/platform/components/{component}/logs/stream:
        get:
            operationId: PlatformComponentService_StreamComponentLogs
components:
    schemas:
        Existing:
            type: object
"""
        result = finalizer.add_platform_component_log_error_schema(text)
        self.assertIn("        PlatformComponentLogError:\n", result)
        self.assertIn("                code:\n                    type: string\n", result)
        # 旧格式：code 是字符串，不能写成 integer。
        self.assertNotIn("                code:\n                    type: integer\n", result)
        with self.assertRaises(SystemExit):
            finalizer.add_platform_component_log_error_schema(result)

    def test_implementation_mode_is_required_and_explicit_in_public_document(self):
        # Read the real generated public operation. The runtime's mandatory
        # selector must not be documented as optional by the pinned generator.
        document = (Path(__file__).resolve().parents[2] / "app/admin/service/cmd/server/assets/openapi.yaml").read_text()
        operation = document.split("    /api/v1/modeldev/implementations:\n", 1)[1].split("\n    /", 1)[0]
        parameter = operation.split("- name: device_mode\n", 1)[1].split("            responses:", 1)[0]
        self.assertIn("required: true", parameter)
        self.assertIn("- cpu", parameter)
        self.assertIn("- gpu", parameter)


if __name__ == '__main__':
    unittest.main()
