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
        for usage in ["CONTAINER_INSTANCE", "TRAINING", "INFERENCE", "VIRTUAL_MACHINE"]:
            self.assertIn("- " + usage, result)
        with self.assertRaises(SystemExit):
            finalizer.require_compute_usage(text, 'string usage = 7;')


if __name__ == '__main__':
    unittest.main()
