#!/usr/bin/env python3
"""Isolated AK/SK acceptance. Never use a shared namespace.

All credentials stay under the explicitly selected RUN/private (0700).
"""
import argparse
import hashlib
import http.client
import importlib.util
import json
import os
import re
from pathlib import Path
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

HERE = Path(__file__).resolve().parent
GOV = HERE.parents[2]
spec = importlib.util.spec_from_file_location('aksk_client', GOV / 'scripts/ops/aksk_vpc_client.py')
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)
HELP = '--help' in sys.argv or '-h' in sys.argv
def required(name):
    value = os.environ.get(name)
    if not value and not HELP:
        raise SystemExit(f'{name} must be explicitly set for this experiment')
    return value or 'help-only'

NS = required('ANI_AKSK_LAB_NAMESPACE')
CONTEXT = required('ANI_AKSK_LAB_CONTEXT')
NODEPORT = int(required('ANI_AKSK_LAB_NODEPORT')) if not HELP else 0
FORWARD_PORT = int(required('ANI_AKSK_LAB_FORWARD_PORT')) if not HELP else 0
R = Path(required('ANI_AKSK_LAB_RUN'))
P = R / 'private'
E = R / 'evidence'
if not HELP:
    P.mkdir(parents=True, exist_ok=True, mode=0o700)
    P.chmod(0o700)
    E.mkdir(parents=True, exist_ok=True)
STATE = P / 'state.json'
S = json.loads(STATE.read_text()) if STATE.exists() else {}


def save():
    STATE.write_text(json.dumps(S, indent=2))
    STATE.chmod(0o600)


def run(args, data=None, **kw):
    result = subprocess.run(args, input=data, text=True, capture_output=True, **kw)
    if result.returncode:
        # stderr can contain credentials from failed SQL/config; keep it private.
        error = P / 'last-command-error.txt'
        error.write_text(result.stdout + result.stderr)
        error.chmod(0o600)
        raise RuntimeError(f'command {args[0]} failed; restricted details: {error}')
    return result.stdout.strip()


def kub(*args, data=None):
    return run(['kubectl', '--context', CONTEXT, '-n', NS, *args], data)


def apply(items):
    kub('apply', '-f', '-', data=json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': items}))


def check(name, ok, **detail):
    row = dict(case=name, status='pass' if ok else 'fail', **detail)
    with (E / 'acceptance.jsonl').open('a') as f:
        f.write(json.dumps(row) + '\n')
    print(json.dumps(row), flush=True)
    if not ok:
        raise AssertionError(name)


def sql(db, text, *args):
    return kub('exec', '-i', 'deploy/postgres', '--', 'psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', db, *args, data=text)


def ready(name):
    kub('rollout', 'status', 'deployment/' + name, '--timeout=150s')
    if name == 'governance' and S.get('base'):
        for _ in range(30):
            try:
                urllib.request.urlopen(S['base'] + '/api/v1/admin/me', timeout=2).close()
            except urllib.error.HTTPError as exc:
                if exc.code == 401:
                    return
            except (urllib.error.URLError, ConnectionError):
                pass
            time.sleep(1)
        raise RuntimeError('Governance NodePort did not become ready')


def restart():
    old = P / ('governance-log-' + str(time.time_ns()) + '.txt')
    old.write_text(kub('logs', 'deployment/governance'))
    old.chmod(0o600)
    kub('rollout', 'restart', 'deployment/governance')
    ready('governance')


def obj(kind, name, **fields):
    return dict(apiVersion='v1', kind=kind, metadata=dict(name=name, namespace=NS), **fields)


def service(name, ports, nodeport=None):
    ps = [{'name': 'p' + str(p), 'port': p, 'targetPort': p} for p in ports]
    if nodeport:
        ps[0]['nodePort'] = nodeport
    return obj('Service', name, spec={'type': 'NodePort' if nodeport else 'ClusterIP', 'selector': {'app': name}, 'ports': ps})


def deployment(name, image, ports, **container):
    volumes = container.pop('volumes', [])
    c = dict(name=name, image=image, imagePullPolicy='Never', ports=[{'containerPort': p} for p in ports],
             resources={'requests': {'cpu': '50m', 'memory': '128Mi'}, 'limits': {'cpu': '1', 'memory': '768Mi'}}, **container)
    return dict(apiVersion='apps/v1', kind='Deployment', metadata=dict(name=name, namespace=NS), spec={
        'replicas': 1, 'strategy': {'type': 'Recreate'}, 'selector': {'matchLabels': {'app': name}},
        'template': {'metadata': {'labels': {'app': name}}, 'spec': {'automountServiceAccountToken': False, 'containers': [c], 'volumes': volumes}}})


def secret_env(name, key):
    return {'name': name, 'valueFrom': {'secretKeyRef': {'name': 'runtime', 'key': key}}}


def env(values):
    return [{'name': k, 'value': v} for k, v in values.items()]


def prepare():
    if S:
        raise RuntimeError('Existing state: refusing to replace lab credentials')
    current = run(['kubectl', 'config', 'current-context'])
    if current != CONTEXT:
        raise RuntimeError('unexpected context')
    spaces = json.loads(run(['kubectl', 'get', 'ns', '-o', 'json']))
    if any(x['metadata']['name'] == NS for x in spaces['items']):
        raise RuntimeError('namespace already exists; inspect ownership before reuse')
    services = json.loads(run(['kubectl', 'get', 'svc', '-A', '-o', 'json']))
    if any(p.get('nodePort') == NODEPORT for x in services['items'] for p in x['spec'].get('ports', [])):
        raise RuntimeError('NodePort occupied')
    S.update(passwords={k: 'Aa9!' + secrets.token_hex(20) for k in ['owner', 'runtime', 'reader', 'platform', 'a', 'b', 'empty']},
             encryption=secrets.token_hex(32), jwt=secrets.token_hex(48), ns=NS, context=CONTEXT)
    (P / 'admin-password').write_text(S['passwords']['platform'])
    (P / 'access-key-encryption').write_text(S['encryption'] + '\n')
    for file in ['admin-password', 'access-key-encryption']:
        (P / file).chmod(0o600)
    def ssl(*args):
        run(['openssl', *args], cwd=P)
    ssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '7', '-subj', '/CN=aksk-lab-ca', '-keyout', 'ca.key', '-out', 'ca.pem')
    for name in ['ani-governance', 'ani-network-service', 'wrong-service']:
        ssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=' + name, '-keyout', name + '.key', '-out', name + '.csr')
        (P / (name + '.ext')).write_text('subjectAltName=DNS:' + name + '\nextendedKeyUsage=' + ('serverAuth' if name == 'ani-network-service' else 'clientAuth') + '\n')
        ssl('x509', '-req', '-in', name + '.csr', '-CA', 'ca.pem', '-CAkey', 'ca.key', '-set_serial', str(secrets.randbits(120)), '-days', '7', '-extfile', name + '.ext', '-out', name + '.pem')
    for file in P.iterdir():
        file.chmod(0o600)
    save()
    run(['kubectl', 'create', 'namespace', NS])
    kub('label', 'namespace', NS, 'ani-task=' + NS)
    items = [obj('Secret', 'runtime', type='Opaque', stringData={
        'owner-password': S['passwords']['owner'], 'admin-password': S['passwords']['platform'],
        'access-key-encryption': S['encryption'] + '\n',
        'governance-dsn': 'postgres://postgres:' + S['passwords']['owner'] + '@postgres:5432/governance?sslmode=disable',
        'network-dsn': 'postgres://postgres:' + S['passwords']['owner'] + '@postgres:5432/network?sslmode=disable',
    })]
    items += [obj('PersistentVolumeClaim', 'postgres', spec={'accessModes': ['ReadWriteOnce'], 'storageClassName': 'standard', 'resources': {'requests': {'storage': '2Gi'}}}), deployment('postgres', 'postgres:18', [5432],
                         env=[secret_env('POSTGRES_PASSWORD', 'owner-password')],
                         volumes=[{'name': 'data', 'persistentVolumeClaim': {'claimName': 'postgres'}}], volumeMounts=[{'name': 'data', 'mountPath': '/var/lib/postgresql'}],
                         readinessProbe={'exec': {'command': ['pg_isready', '-U', 'postgres']}, 'periodSeconds': 2}), service('postgres', [5432]),
              deployment('redis', 'redis:7-alpine', [6379]), service('redis', [6379])]
    apply(items)
    prepare_databases()


def prepare_databases():
    ready('postgres')
    ready('redis')
    sql('postgres', 'CREATE DATABASE governance; CREATE DATABASE network;')
    sql('postgres', "CREATE ROLE governance_runtime LOGIN PASSWORD '" + S['passwords']['runtime'] + "'; CREATE ROLE network_read LOGIN PASSWORD '" + S['passwords']['reader'] + "';")
    node = json.loads(run(['docker', 'inspect', 'kind-test-worker']))[0]
    S['base'] = 'http://' + next(iter(node['NetworkSettings']['Networks'].values()))['IPAddress'] + ':' + str(NODEPORT)
    save()
    (E / 'environment.json').write_text(json.dumps({'hostname': run(['hostname']), 'context': CONTEXT, 'namespace': NS, 'nodeport': NODEPORT, 'base_url': S['base'], 'nodes': json.loads(kub('get', 'nodes', '-o', 'json'))}, indent=2))
    check('isolated-environment', True, namespace=NS, base_url=S['base'])


def configs():
    """Write credential-bearing configuration directly to a Secret, never evidence."""
    import yaml
    config = {'server': {'rest': {'addr': ':7788', 'timeout': '10s', 'enable_swagger': True, 'middleware': {'enable_logging': True, 'enable_recovery': True, 'enable_validate': True, 'enable_metadata': True}}},
              'data': {'database': {'driver': 'postgres', 'source': 'host=postgres port=5432 user=governance_runtime password=' + S['passwords']['runtime'] + ' dbname=governance sslmode=disable', 'migrate': False, 'max_open_connections': 10, 'max_idle_connections': 5},
                       'redis': {'addr': 'redis:6379', 'dial_timeout': '10s', 'read_timeout': '1s', 'write_timeout': '1s'}},
              'authn': {'type': 'jwt', 'jwt': {'method': 'HS256', 'key': S['jwt'], 'access_token_expires': '5400s', 'refresh_token_expires': '43200s'}},
              'authz': {'type': 'casbin', 'casbin': {}}, 'logger': {'type': 'std'}}
    nc = yaml.safe_load(re.sub(r'\$\{[^:}]+:([^}]*)\}', r'\1', (R / 'network/configs/config.yaml').read_text()))
    nc['network']['database_dsn'] = 'postgres://network_read:' + S['passwords']['reader'] + '@postgres:5432/network?sslmode=disable&connect_timeout=2'
    import base64
    nc['network']['cursor_signing_key'] = base64.b64encode(secrets.token_bytes(32)).decode()
    nc['server']['grpc'].update(addr='0.0.0.0:19090', timeout='5s')
    nc['server']['admin'].update(addr='0.0.0.0:19091', timeout='2s')
    tls = {name: (P / name).read_text() for name in ['ca.pem', 'ani-governance.pem', 'ani-governance.key', 'ani-network-service.pem', 'ani-network-service.key']}
    apply([obj('Secret', 'configs', type='Opaque', stringData={'governance.yaml': json.dumps(config), 'network.yaml': json.dumps(nc), **tls})])


def job(name, init_containers, main, volumes, mounts):
    """One-shot Job whose steps run serially: initContainers first, then the main container."""
    return {'apiVersion': 'batch/v1', 'kind': 'Job', 'metadata': {'name': name, 'namespace': NS}, 'spec': {'backoffLimit': 0, 'template': {'spec': {
        'restartPolicy': 'Never', 'automountServiceAccountToken': False, 'volumes': volumes,
        'initContainers': [dict(c, volumeMounts=mounts) for c in init_containers],
        'containers': [dict(main, volumeMounts=mounts)]}}}}


def deploy():
    images = json.loads((R / 'images.json').read_text())
    configs()
    volumes = [{'name': 'config', 'secret': {'secretName': 'configs'}}, {'name': 'runtime', 'secret': {'secretName': 'runtime'}}]
    mounts = [{'name': 'config', 'mountPath': '/config', 'readOnly': True}, {'name': 'runtime', 'mountPath': '/run/secrets', 'readOnly': True}]
    # Atlas and the admin CLI run explicitly before the service deployment, each from its own
    # image: the runtime images are distroless (no shell), so every step is a single container
    # and initContainers give the required ordering. The owner DSN is only in these Jobs.
    dsn = [secret_env('ANI_DATABASE_DSN', 'governance-dsn')]
    base = {'imagePullPolicy': 'Never', 'env': dsn}

    def atlas(name, args):
        return dict(base, name=name, image=images['atlas'], args=list(args) + ['--dir', 'file://migrations', '--url', '$(ANI_DATABASE_DSN)'])

    def admin(name, args):
        return dict(base, name=name, image=images['governance-admin'], command=['/app/bin/admin'], args=list(args))

    migrate = job('governance-migrate',
                  [atlas('status', ['migrate', 'status']), atlas('dry-run', ['migrate', 'apply', '--dry-run'])],
                  atlas('apply', ['migrate', 'apply']), volumes, mounts)
    apply([migrate])
    kub('wait', '--for=condition=complete', 'job/governance-migrate', '--timeout=180s')
    (E / 'governance-migrate.log').write_text(kub('logs', 'job/governance-migrate'))
    bootstrap = job('governance-admin',
                    [admin('init', ['init', '--username', 'admin', '--password-file', '/run/secrets/admin-password'])],
                    admin('check', ['check']), volumes, mounts)
    apply([bootstrap])
    kub('wait', '--for=condition=complete', 'job/governance-admin', '--timeout=180s')
    (E / 'governance-admin.log').write_text(kub('logs', 'job/governance-admin'))
    sql('governance', 'GRANT USAGE ON SCHEMA public TO governance_runtime; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO governance_runtime; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO governance_runtime; REVOKE CREATE ON SCHEMA public FROM PUBLIC;')
    migration = {'apiVersion': 'batch/v1', 'kind': 'Job', 'metadata': {'name': 'network-migrate', 'namespace': NS}, 'spec': {'backoffLimit': 0, 'template': {'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False, 'containers': [{
        'name': 'migration', 'image': images['network'], 'imagePullPolicy': 'Never', 'args': ['-migrate'], 'env': [secret_env('ANI_NETWORK_MIGRATION_DSN', 'network-dsn'), {'name': 'ANI_NETWORK_RUNTIME_ROLE', 'value': 'network_read'}]}]}}}}
    apply([migration])
    kub('wait', '--for=condition=complete', 'job/network-migrate', '--timeout=120s')
    (E / 'network-migrate.log').write_text(kub('logs', 'job/network-migrate'))
    sql('network', 'REVOKE INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER ON ALL TABLES IN SCHEMA public FROM network_read;')
    items = [deployment('network', images['network'], [19090, 19091], args=['-conf', '/config/network.yaml'], volumes=volumes, volumeMounts=mounts,
                        env=env({'ANI_NETWORK_MODE': 'vpc-read', 'ANI_NETWORK_CLIENT_CA': '/config/ca.pem', 'ANI_NETWORK_TLS_CERT': '/config/ani-network-service.pem', 'ANI_NETWORK_TLS_KEY': '/config/ani-network-service.key'}),
                        readinessProbe={'httpGet': {'path': '/readyz', 'port': 19091}, 'periodSeconds': 2}), service('network', [19090, 19091]),
             deployment('governance', images['governance'], [7788], command=['/app/bin/server'], args=['-c', '/governance'], volumes=volumes, volumeMounts=mounts + [{'name': 'config', 'mountPath': '/governance/config.yaml', 'subPath': 'governance.yaml', 'readOnly': True}],
                        env=env({'ANI_ACCESS_KEY_ENCRYPTION_KEY_FILE': '/run/secrets/access-key-encryption', 'ANI_NETWORK_ADDR': 'network:19090', 'ANI_NETWORK_CA': '/config/ca.pem', 'ANI_NETWORK_CERT': '/config/ani-governance.pem', 'ANI_NETWORK_KEY': '/config/ani-governance.key', 'ANI_NETWORK_TIMEOUT': '3s'}),
                        readinessProbe={'tcpSocket': {'port': 7788}, 'periodSeconds': 2}), service('governance', [7788], NODEPORT)]
    apply(items)
    ready('network')
    ready('governance')
    check('explicit-empty-database-initialization', True)


def request(path, token=None, data=None, method=None, headers=None):
    h = {'Content-Type': 'application/json'}
    if token:
        h['Authorization'] = 'Bearer ' + token
    h.update(headers or {})
    if 'X-Signature' in h:
        S.setdefault('request_signatures', []).append(h['X-Signature'])
        save()
    req = urllib.request.Request(S['base'] + path, data=json.dumps(data).encode() if data is not None else None, headers=h, method=method)
    try:
        with urllib.request.build_opener(client.NoRedirect()).open(req, timeout=15) as resp:
            return resp.status, json.load(resp)
    except urllib.error.HTTPError as exc:
        try:
            body = json.load(exc)
        except ValueError:
            body = {}
        return exc.code, body


def login(label):
    data = {'username': 'admin', 'password': S['passwords'][label]}
    if label != 'platform':
        data['tenant_name'] = 'aksk-' + label
    status, body = request('/api/v1/auth/' + ('platform/' if label == 'platform' else '') + 'password/login', data=data)
    check('login-' + label, status == 200 and bool(body.get('access_token')), http=status)
    S.setdefault('tokens', {})[label] = body['access_token']
    save()


def setup():
    login('platform')
    t = S['tokens']['platform']
    status, _ = request('/api/v1/admin/plans', t, {'data': {'name': 'AKSK isolated full', 'version': 'FREE', 'expiryPolicy': 'READONLY'}})
    check('create-dedicated-plan', status == 200, http=status)
    plan = int(sql('governance', "SELECT id FROM sys_plans WHERE name='AKSK isolated full';"))
    for module in ['DASHBOARD', 'OPM', 'SYSTEM', 'NETWORK']:
        status, _ = request('/api/v1/admin/plan-modules', t, {'data': {'planId': plan, 'module': module}})
        check('plan-module-' + module, status == 200, http=status)
    status, _ = request('/api/v1/admin/plans', t, {'data': {'name': 'AKSK isolated no-network', 'version': 'FREE', 'expiryPolicy': 'READONLY'}})
    check('create-no-network-plan', status == 200, http=status)
    basic = int(sql('governance', "SELECT id FROM sys_plans WHERE name='AKSK isolated no-network';"))
    for module in ['DASHBOARD', 'OPM', 'SYSTEM']:
        status, _ = request('/api/v1/admin/plan-modules', t, {'data': {'planId': basic, 'module': module}})
        check('no-network-plan-module-' + module, status == 200, http=status)
    S['tenants'] = {}
    for label in ['a', 'b', 'empty']:
        status, _ = request('/api/v1/admin/tenants:with-admin', t, {'tenant': {'name': 'AKSK ' + label, 'code': 'aksk-' + label, 'status': 'ON', 'plan_id': plan}, 'user': {'username': 'admin', 'status': 'NORMAL'}, 'password': S['passwords'][label], 'activation_mode': 'IMMEDIATE'})
        check('create-tenant-' + label, status == 200, http=status)
        tenant = json.loads(sql('governance', "SELECT row_to_json(x) FROM (SELECT id,resource_tenant_id AS uuid FROM sys_tenants WHERE code='aksk-" + label + "') x;"))
        tenant['role'] = int(sql('governance', 'SELECT id FROM sys_roles WHERE tenant_id=' + str(tenant['id']) + ' ORDER BY id LIMIT 1;'))
        S['tenants'][label] = tenant
        save()
        if label != 'empty':
            sql('governance', (GOV / 'scripts/ops/sql/bootstrap-network-access.sql').read_text(), '-v', 'tenant_id=' + str(tenant['id']), '-v', 'role_id=' + str(tenant['role']))
            vid = 'vpc_' + uuid.uuid5(uuid.NAMESPACE_URL, NS + '/' + label).hex
            operation = str(uuid.uuid5(uuid.NAMESPACE_URL, NS + '/op/' + label))
            tenant['vpc'] = vid
            sql('network', "BEGIN; INSERT INTO network_vpcs(tenant_id,vpc_id,name,description,cidr,state,created_at,updated_at,last_operation_id) VALUES('" + tenant['uuid'] + "','" + vid + "','aksk-" + label + "','isolated persistent query fixture','10." + ('20' if label == 'a' else '21') + ".0.0/16','available',now(),now(),'" + operation + "'); INSERT INTO network_operations(tenant_id,operation_id,vpc_id,kind,state,created_at,updated_at,completed_at) VALUES('" + tenant['uuid'] + "','" + operation + "','" + vid + "','create_vpc','succeeded',now(),now(),now()); COMMIT;")
    save()
    restart()
    setup_roles()


def setup_roles():
    login('platform')
    t = S['tokens']['platform']
    basic = int(sql('governance', "SELECT id FROM sys_plans WHERE name='AKSK isolated no-network';"))
    for label in ['a', 'b', 'empty']:
        login(label)
        for name in ['reader', 'denied']:
            status, _ = request('/api/v1/admin/roles', t, {'data': {'tenant_id': S['tenants'][label]['id'], 'name': name, 'code': 'tenant:' + label + ':' + name, 'status': 'ON', 'type': 'TENANT', 'dataScope': 'ALL'}})
            check('create-role-' + label + '-' + name, status == 200, http=status)
            S['tenants'][label][name] = int(sql('governance', "SELECT id FROM sys_roles WHERE tenant_id=" + str(S['tenants'][label]['id']) + " AND name='" + name + "';"))
        sql('governance', (GOV / 'scripts/ops/sql/bootstrap-network-access.sql').read_text(), '-v', 'tenant_id=' + str(S['tenants'][label]['id']), '-v', 'role_id=' + str(S['tenants'][label]['reader']))
    status, _ = request('/api/v1/admin/tenants/' + str(S['tenants']['empty']['id']), t, {'data': {'plan_id': basic}, 'updateMask': 'planId'}, method='PUT')
    check('assign-no-network-plan', status == 200, http=status)
    save()
    restart()
    check('persistent-vpc-fixtures', sql('network', 'SELECT count(*) FROM network_vpcs;') == '2', data_plane='not_verified')


def create_key(label='a', role=None, name='vpc-reader', expires=None):
    data = {'name': name, 'role_id': role or S['tenants'][label]['reader']}
    if expires:
        data['expires_at'] = expires
    status, body = request('/api/v1/auth/api-keys', S['tokens'][label], {'data': data})
    check('key-create-' + label + '-' + name, status == 201 and bool(body.get('secret_key')) and isinstance(body.get('data', {}).get('id'), int), http=status)
    S.setdefault('all_secrets', []).append(body['secret_key'])
    key = {'id': body['data']['id'], 'ak': body['data']['access_key'], 'sk': body['secret_key']}
    save()
    return key


def signed(key, vpc=None, timestamp=None, path=None, headers=None, method='GET', body=None):
    p, h = client.sign_vpc(key['ak'], key['sk'], vpc or S['tenants']['a']['vpc'], timestamp)
    if headers:
        h.update(headers)
    return request(path or p, data=body, method=method, headers=h)


def key_update(key, data, mask, want=200, label='a'):
    status, body = request('/api/v1/auth/api-keys/' + str(key['id']), S['tokens'][label], {'data': data, 'update_mask': mask}, method='PUT')
    check('key-update-' + mask, status == want, http=status)
    return body


def contract():
    for label in ['platform', 'a', 'b', 'empty']:
        login(label)
    key = create_key()
    S['key'] = key
    save()
    os.environ['ANI_ALLOW_HTTP_FOR_TEST'] = '1'
    result = client.get_vpc(S['base'], key['ak'], key['sk'], S['tenants']['a']['vpc'])
    row = json.loads(sql('network', "SELECT row_to_json(x) FROM (SELECT vpc_id,name,cidr,state,version FROM network_vpcs WHERE tenant_id='" + S['tenants']['a']['uuid'] + "') x;"))
    v = result['vpc']
    check('python-nodeport-governance-mtls-network-pg', v['id'] == row['vpc_id'] and all(str(v[k]) == str(row[k]) for k in ['name', 'cidr', 'state', 'version']), vpc_id=v['id'])
    (E / 'vpc-a.json').write_text(json.dumps(result, indent=2))
    check('repeat-read-within-window', signed(key)[0] == 200)
    status, result = request('/api/v1/networks/vpcs/' + S['tenants']['a']['vpc'], S['tokens']['a'])
    check('user-jwt-vpc-query', status == 200 and result.get('vpc', {}).get('id') == v['id'], http=status)
    status, result = signed(key, headers={'X-Ani-Tenant-Id': S['tenants']['b']['uuid'], 'X-Ani-Actor': 'governance:user:1'})
    check('public-identity-headers-overwritten', status == 200 and result.get('vpc', {}).get('id') == v['id'], http=status)
    cross = signed(key, S['tenants']['b']['vpc'])
    absent = signed(key, 'vpc_' + '9' * 32)
    check('cross-tenant-same-as-absent', cross[0] == absent[0] == 404 and all(cross[1].get(k) == absent[1].get(k) for k in ['code', 'reason', 'message']) and {k: x for k, x in cross[1].get('metadata', {}).items() if k != 'request_id'} == {k: x for k, x in absent[1].get('metadata', {}).items() if k != 'request_id'}, http=cross[0])
    cases = [
        ('bad-sk', {'sk': 'sk-wrong'}, {}, 401),
        ('bad-signature', {}, {'headers': {'X-Signature': '0' * 64}}, 401),
        ('tampered-path', {}, {'path': '/api/v1/networks/vpcs/' + S['tenants']['b']['vpc']}, 401),
        ('tampered-time', {}, {'headers': {'X-Timestamp': str(int(time.time()) - 10)}}, 401),
        ('expired-window', {}, {'timestamp': int(time.time()) - 301}, 401),
        ('future-window', {}, {'timestamp': int(time.time()) + 305}, 401),
        ('query', {}, {'path': '/api/v1/networks/vpcs/' + v['id'] + '?x=1'}, 400),
        ('body', {}, {'body': {'x': 1}}, 400),
        ('mixed-credentials', {}, {'headers': {'Authorization': 'Bearer ' + S['tokens']['a']}}, 400),
        ('user-only', {}, {'path': '/api/v1/admin/me'}, 403),
        ('key-management', {}, {'path': '/api/v1/auth/api-keys'}, 403),
    ]
    for name, changed, args, want in cases:
        status, _ = signed({**key, **changed}, **args)
        check(name, status == want, http=status)
    origin = urllib.parse.urlsplit(S['base'])
    for field in ['X-Access-Key', 'X-Timestamp', 'X-Signature']:
        for mode in ['missing', 'duplicate', 'comma']:
            path, headers = client.sign_vpc(key['ak'], key['sk'], v['id'])
            conn = http.client.HTTPConnection(origin.hostname, origin.port, timeout=10)
            conn.putrequest('GET', path)
            for name, value in headers.items():
                if name == field and mode == 'missing':
                    continue
                conn.putheader(name, value + ',' + value if name == field and mode == 'comma' else value)
                if name == field and mode == 'duplicate':
                    conn.putheader(name, value)
            conn.endheaders()
            response = conn.getresponse()
            response.read()
            check('header-' + field + '-' + mode, response.status == 401, http=response.status)
            conn.close()
    denied = create_key(role=S['tenants']['a']['denied'], name='denied')
    check('no-role-permission', signed(denied)[0] == 403)
    empty = create_key(label='empty', name='no-network')
    check('no-network-plan', signed(empty)[0] == 403)
    for state, want in [('OFF', 403), ('ON', 200)]:
        status, _ = request('/api/v1/admin/tenants/' + str(S['tenants']['a']['id']), S['tokens']['platform'], {'data': {'status': state}, 'updateMask': 'status'}, method='PUT')
        check('set-tenant-' + state, status == 200, http=status)
        check('tenant-state-' + state, signed(key)[0] == want)
    status, _ = request('/api/v1/auth/api-keys', S['tokens']['a'], {'data': {'name': 'foreign-role', 'role_id': S['tenants']['b']['reader']}})
    check('cross-tenant-role-create', status in [400, 403], http=status)
    key_update(key, {'role_id': S['tenants']['b']['reader']}, 'roleId', want=403)
    for method, data in [('GET', None), ('PUT', {'data': {'name': 'foreign'}, 'update_mask': 'name'}), ('DELETE', None)]:
        status, _ = request('/api/v1/auth/api-keys/' + str(key['id']), S['tokens']['b'], data, method=method)
        check('cross-tenant-key-' + method, status == 404, http=status)
    key_update(key, {'is_active': False}, 'isActive')
    check('disabled-key', signed(key)[0] == 401)
    key_update(key, {'is_active': True}, 'isActive')
    check('enabled-key', signed(key)[0] == 200)
    key_update(key, {'expires_at': '2020-01-01T00:00:00Z'}, 'expiresAt')
    check('expired-key', signed(key)[0] == 401)
    key_update(key, {'expires_at': None}, 'expiresAt')
    check('clear-expiration', signed(key)[0] == 200)
    key_update(key, {'role_id': S['tenants']['a']['denied']}, 'roleId')
    check('rebind-denied-role', signed(key)[0] == 403)
    key_update(key, {'role_id': S['tenants']['a']['reader']}, 'roleId')
    check('rebind-reader-role', signed(key)[0] == 200)
    role = S['tenants']['a']['reader']
    status, _ = request('/api/v1/admin/roles/' + str(role), S['tokens']['platform'], {'data': {'status': 'OFF'}, 'updateMask': 'status'}, method='PUT')
    check('disable-bound-role', status == 200, http=status)
    check('disabled-role-request', signed(key)[0] == 403)
    check('disabled-role-bad-signature', signed({**key, 'sk': 'sk-wrong'})[0] == 401)
    status, _ = request('/api/v1/auth/api-keys', S['tokens']['a'], {'data': {'name': 'off-role', 'role_id': role}})
    check('disabled-role-create', status in [400, 403], http=status)
    status, _ = request('/api/v1/admin/roles/' + str(role), S['tokens']['platform'], {'data': {'status': 'ON'}, 'updateMask': 'status'}, method='PUT')
    check('restore-bound-role', status == 200, http=status)
    check('restored-role-request', signed(key)[0] == 200)
    status, rotated = request('/api/v1/auth/api-keys/' + str(key['id']) + '/secret', S['tokens']['a'], {}, method='PUT')
    check('reset-response', status == 200 and bool(rotated.get('secret_key')) and rotated.get('data', {}).get('access_key') == key['ak'], http=status)
    check('reset-old-secret-rejected', signed(key)[0] == 401)
    key['sk'] = rotated['secret_key']
    S['all_secrets'].append(key['sk'])
    S['key'] = key
    save()
    check('reset-new-secret-accepted', signed(key)[0] == 200)
    doomed = create_key(name='delete')
    status, body = request('/api/v1/auth/api-keys/' + str(doomed['id']), S['tokens']['a'], method='DELETE')
    check('delete-response', status == 200 and body == {'status': 'revoked'}, http=status)
    check('deleted-key-rejected', signed(doomed)[0] == 401)
    for path in ['/api/v1/auth/api-keys', '/api/v1/auth/api-keys/' + str(key['id'])]:
        status, body = request(path, S['tokens']['a'])
        raw = json.dumps(body)
        check('no-secret-' + ('list' if path.endswith('keys') else 'detail'), status == 200 and not any(sk in raw for sk in S['all_secrets']) and all(term not in raw for term in ['secret_key', 'secret_hash', 'secret_ciphertext']), http=status)
    status, _ = request('/api/v1/admin/access-keys/token', data={'access_key': key['ak'], 'secret_key': 'not-a-secret'})
    check('exchange-route-removed', status == 404, http=status)


def mtls():
    # Use a fresh port-forward per TLS case: a rejected TLS connection can close
    # kubectl's forwarding stream. Prove a positive control before each negative.
    base = [str(R / 'bin/network-probe'), '-address', f'127.0.0.1:{FORWARD_PORT}', '-ca', str(P / 'ca.pem'), '-tenant', S['tenants']['a']['uuid'], '-vpc', S['tenants']['a']['vpc']]
    cases = [('user', 'ani-governance', 'governance:user:7', 'OK', []), ('key', 'ani-governance', 'governance:access-key:' + str(S['key']['id']), 'OK', []), ('missing-cert', '', 'governance:user:7', 'Unavailable', []), ('wrong-cert', 'wrong-service', 'governance:user:7', 'Unavailable', []), ('tenant-mismatch', 'ani-governance', 'governance:user:7', 'PermissionDenied', ['-header-tenant', S['tenants']['b']['uuid']])]
    for name, cert, actor, want, extra in cases:
        forward = subprocess.Popen(['kubectl', '--context', CONTEXT, '-n', NS, 'port-forward', 'svc/network', f'{FORWARD_PORT}:19090'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            time.sleep(2)
            if forward.poll() is not None:
                raise RuntimeError('Network port-forward failed')
            control = json.loads(run(base + ['-actor', 'governance:user:7', '-want', 'OK', '-cert', str(P / 'ani-governance.pem'), '-key', str(P / 'ani-governance.key')]))
            check('mtls-control-' + name, control.get('code') == 'OK')
            args = base + ['-actor', actor, '-want', want] + extra
            if cert:
                args += ['-cert', str(P / (cert + '.pem')), '-key', str(P / (cert + '.key'))]
            result = json.loads(run(args))
            check('mtls-' + name, result.get('code') == want, grpc=result.get('code'))
        finally:
            forward.terminate()
            forward.wait(timeout=10)


def final_checks():
    # Read-only owner observation: runtime role lacks DDL, restart must preserve schema and seed tables.
    def snapshot():
        schema = kub('exec', 'deploy/postgres', '--', 'pg_dump', '-U', 'postgres', '-d', 'governance', '--schema-only')
        # pg_dump 18 emits random restrict tokens; exclude those from the structural hash.
        schema = '\n'.join(l for l in schema.splitlines() if not l.startswith(('\\restrict ', '\\unrestrict ')))
        tables = ['sys_tenants', 'sys_users', 'sys_roles', 'sys_plans', 'sys_plan_modules', 'sys_permissions', 'sys_permission_apis', 'sys_role_permissions', 'sys_apis', 'sys_configs', 'sys_user_credentials']
        rows = {table: sql('governance', "SELECT md5(COALESCE(string_agg(row_to_json(t)::text, chr(10) ORDER BY row_to_json(t)::text),'')) FROM " + table + ' t;') for table in tables}
        return {'schema_sha256': hashlib.sha256(schema.encode()).hexdigest(), 'tables': rows}
    before = snapshot()
    restart()
    after = snapshot()
    check('restart-no-schema-or-initialization-writes', before == after)
    (E / 'restart-snapshot.json').write_text(json.dumps({'before': before, 'after': after}, indent=2))
    # The runtime image has no shell, so the probe is a dedicated Pod that simply omits the
    # master key env var instead of unsetting it inside a shell.
    images = json.loads((R / 'images.json').read_text())
    probe = {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'missing-master-key', 'namespace': NS}, 'spec': {
        'restartPolicy': 'Never', 'automountServiceAccountToken': False,
        'volumes': [{'name': 'config', 'secret': {'secretName': 'configs'}}],
        'containers': [{'name': 'probe', 'image': images['governance'], 'imagePullPolicy': 'Never',
                        'command': ['/app/bin/server'], 'args': ['-c', '/governance'],
                        'volumeMounts': [{'name': 'config', 'mountPath': '/governance/config.yaml', 'subPath': 'governance.yaml', 'readOnly': True}]}]}}
    apply([probe])
    subprocess.run(['kubectl', '--context', CONTEXT, '-n', NS, 'wait', '--for=jsonpath={.status.phase}=Failed',
                    'pod/missing-master-key', '--timeout=60s'], capture_output=True, text=True)
    rejected = subprocess.run(['kubectl', '--context', CONTEXT, '-n', NS, 'logs', 'pod/missing-master-key'], capture_output=True, text=True, timeout=60)
    kub('delete', 'pod/missing-master-key', '--ignore-not-found=true')
    check('missing-master-key-startup-refused', 'ANI_ACCESS_KEY_ENCRYPTION_KEY_FILE' in rejected.stdout + rejected.stderr)
    check('runtime-no-ddl', sql('governance', "SELECT has_schema_privilege('governance_runtime','public','CREATE');") == 'f')
    check('key-persists-after-restart', signed(S['key'])[0] == 200)
    fk_sql = "DO $$ BEGIN BEGIN UPDATE sys_access_keys SET role_id=" + str(S['tenants']['b']['reader']) + " WHERE id=" + str(S['key']['id']) + "; RAISE EXCEPTION 'cross-tenant FK accepted'; EXCEPTION WHEN foreign_key_violation THEN NULL; END; BEGIN DELETE FROM sys_roles WHERE id=" + str(S['tenants']['a']['reader']) + "; RAISE EXCEPTION 'referenced role deletion accepted'; EXCEPTION WHEN foreign_key_violation OR restrict_violation THEN NULL; END; END $$;"
    sql('governance', fk_sql)
    check('postgres-tenant-role-fk-and-delete-restrict', True)
    check('database-only-encrypted-sk', sql('governance', "SELECT count(*) FROM sys_access_keys WHERE secret_ciphertext NOT LIKE 'enc:%' OR secret_ciphertext IS NULL;") == '0')
    # Failed ciphertext never falls back to cleartext. Restore the original restricted value.
    kid = S['key']['id']
    encrypted = sql('governance', 'SELECT secret_ciphertext FROM sys_access_keys WHERE id=' + str(kid) + ';')
    try:
        sql('governance', "UPDATE sys_access_keys SET secret_ciphertext='invalid' WHERE id=" + str(kid) + ';')
        check('bad-ciphertext-fails-closed', signed(S['key'])[0] == 503)
    finally:
        sql('governance', "UPDATE sys_access_keys SET secret_ciphertext='" + encrypted.replace("'", "''") + "' WHERE id=" + str(kid) + ';')
    for label in ['a', 'platform']:
        login(label)
        token = S['tokens'][label]
        status, body = request('/api/v1/auth/logout', token, {})
        check('logout-' + label, status == 200 and body.get('status') == 'revoked', http=status)
        status, _ = request('/api/v1/admin/me', token)
        check('logout-revokes-jwt-' + label, status == 401, http=status)
    check('logout-does-not-impersonate-key', signed(S['key'])[0] == 200)
    login('a')
    time.sleep(2)
    logs = kub('logs', 'deployment/governance') + ''.join(f.read_text() for f in P.glob('governance-log-*.txt'))
    audits = sql('governance', 'SELECT row_to_json(t) FROM sys_api_audit_logs t;')
    check('no-sk-in-logs-or-audits', not any(sk in logs + audits for sk in S['all_secrets']))
    check('no-signatures-in-logs-or-audits', not any(sig in logs + audits for sig in S.get('request_signatures', [])))
    check('audits-have-key-and-user', all(sql('governance', "SELECT count(*) FROM sys_api_audit_logs WHERE subject_type='" + kind + "';") not in ['', '0'] for kind in ['user', 'api_key']))
    # Only sanitized evidence leaves private storage.
    (E / 'audit-subject-counts.json').write_text(sql('governance', 'SELECT json_agg(x) FROM (SELECT subject_type,count(*) FROM sys_api_audit_logs GROUP BY subject_type) x;'))
    pods = json.loads(kub('get', 'pods', '-o', 'json'))
    (E / 'images.json').write_text(json.dumps([{'pod': p['metadata']['name'], 'containers': [{'name': c['name'], 'image': c['image'], 'imageID': c.get('imageID')} for c in p.get('status', {}).get('containerStatuses', [])]} for p in pods['items']], indent=2))


def security_checks():
    kid = S['key']['id']
    encrypted = sql('governance', 'SELECT secret_ciphertext FROM sys_access_keys WHERE id=' + str(kid) + ';')
    try:
        for name, bad in [('missing', ''), ('plaintext', 'sk-public-invalid-fixture'), ('malformed', 'enc:bad')]:
            sql('governance', "UPDATE sys_access_keys SET secret_ciphertext='" + bad + "' WHERE id=" + str(kid) + ';')
            check(name + '-ciphertext-fails-closed', signed(S['key'])[0] == 503)
    finally:
        sql('governance', "UPDATE sys_access_keys SET secret_ciphertext='" + encrypted.replace("'", "''") + "' WHERE id=" + str(kid) + ';')
    check('ciphertext-restored', signed(S['key'])[0] == 200)
    check('api-key-audit-no-user-impersonation', sql('governance', "SELECT count(*) FROM sys_api_audit_logs WHERE subject_type='api_key' AND (subject_id IS NULL OR subject_id=0 OR user_id IS NOT NULL OR tenant_id IS NULL OR tenant_id=0);") == '0')
    check('failed-signature-audit-unverified', sql('governance', "SELECT count(*) FROM sys_api_audit_logs WHERE status_code=401 AND path='/api/v1/networks/vpcs/{vpc_id}' AND (subject_type IS NOT NULL OR subject_id IS NOT NULL OR user_id IS NOT NULL);") == '0')
    check('last-used-is-successful-signature-time', sql('governance', 'SELECT last_used_at IS NOT NULL FROM sys_access_keys WHERE id=' + str(kid) + ';') == 't')


def diagnose():
    sensitive = list(S.get('passwords', {}).values()) + list(S.get('tokens', {}).values()) + S.get('all_secrets', []) + S.get('request_signatures', []) + [S.get('jwt', ''), S.get('encryption', '')]
    for target in ['job/governance-migrate', 'job/governance-admin', 'job/network-migrate', 'deployment/governance', 'deployment/network']:
        result = subprocess.run(['kubectl', '--context', CONTEXT, '-n', NS, 'logs', target, '--tail=60'], text=True, capture_output=True)
        output = result.stdout + result.stderr
        for secret in sensitive:
            if secret:
                output = output.replace(secret, '[redacted]')
        print(target + '\n' + output)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('stage', choices=['prepare', 'prepare_databases', 'deploy', 'setup', 'setup_roles', 'contract', 'mtls', 'final_checks', 'security_checks', 'diagnose'])
    args = parser.parse_args()
    globals()[args.stage]()
