#!/usr/bin/env python3
"""Generate reviewed XRPC schemas, Go transport metadata and TypeScript bindings.

The definitions below are the public transport views of the current application
operations. Arbitrary authored schema/frontmatter JSON retains its explicit JSON
string representation. Decimal measurements are strings because Lexicon has no
floating-point data type. Runtime adapters preserve REST's original representation.
"""
import argparse
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]
PREFIX = 'dev.mycontextprotocol.'
DEFS = PREFIX + 'types.defs'

def string(**kw): return {'type': 'string', **kw}
def integer(**kw): return {'type': 'integer', **kw}
def boolean(): return {'type': 'boolean'}
def array(items, **kw): return {'type': 'array', 'items': items, **kw}
def obj(properties, required=()):
    value = {'type': 'object', 'properties': properties}
    if required: value['required'] = list(required)
    return value
def ref(name): return {'type': 'ref', 'ref': DEFS + '#' + name}
def strings(): return array(string())
def fields(names, kind=string): return {key: kind() for key in names.split()}
DECIMAL = string(description='Finite base-10 decimal string; REST retains numeric values.')

D = {}
D['project'] = obj({**fields('id account_id name slug subdomain created_at custom_domain custom_domain_verified_at active_release_id mcp_url suspended_at'), 'mcp_oauth_enabled': boolean()}, 'id account_id name slug subdomain created_at'.split())
D['apiKey'] = obj({**fields('id project_id name key_prefix status created_at last_used_at')}, ['id','project_id','key_prefix','status','created_at'])
D['createdKey'] = obj(fields('id key prefix name'), ['id','key','prefix'])
D['release'] = obj({**fields('id project_id commit_sha status error_summary created_at'), **fields('is_active',boolean), **fields('mcp_metadata_blocking_skills mcp_metadata_warning_skills skill_body_changes_count',integer)}, ['id','project_id','commit_sha','status','created_at'])
D['validationIssue'] = obj({**fields('path message code summary fix_hint'), 'line': integer()}, ['path','message'])
D['validation'] = obj({'is_valid':boolean(),'errors':array(ref('validationIssue')),'warnings':array(ref('validationIssue'))}, ['is_valid','errors','warnings'])
D['routing'] = obj({'use_when':strings(),'avoid_when':strings(),'failure_modes':strings(),'invoke_first':boolean()})
D['activation'] = obj({'mode':string(),'intents':strings(),'events':strings(),'tags':strings(),'examples':strings()}, ['mode'])
D['requirement'] = obj({'capability':string(),'required':boolean(),'on_missing':string()}, ['capability','required','on_missing'])
D['runtimePatch'] = obj({**fields('id kind scope enforcement version lifecycle'), 'priority':integer(),'activation':ref('activation'),'requires':array(ref('requirement')),'conflicts_with':strings()})
D['clarification'] = obj(fields('field question reason'))
D['compiledSkill'] = obj({**fields('id release_id skill_package_id path name summary skill_body exposure_type risk_level status skill_id kind scope activation_mode enforcement version source_checksum canonical_json body_diff_unified body_diff_prior_release_id schema_json'), **fields('canonical_schema_version priority',integer), **fields('yaml_frontmatter_present repo_specific clarification_required invoke_first',boolean),'clarification_questions':array(ref('clarification')),'use_when':strings(),'avoid_when':strings(),'failure_modes':strings()}, ['id','release_id','path','name','status'])
D['proposal'] = obj({**fields('status pr_url pull_request_url branch repo_owner repo_name commit_sha message source_path'), **fields('changed',boolean)})
D['requestLog'] = obj({**fields('id project_id release_id timestamp client_id method error_code error_message'), **fields('latency_ms status',integer)}, ['id','project_id','timestamp','method','status'])
D['repositoryConnection'] = obj({**fields('project_id provider repo_owner repo_name default_branch auth_type webhook_id repository_did owner_did owner_handle knot_url'), 'github_installation_configured':boolean(), 'is_private':boolean()}, ['project_id','provider','repo_owner','repo_name','default_branch','auth_type'])
D['repository'] = obj({**fields('id owner owner_login name full_name default_branch html_url provider repository_did owner_did knot_url description'), **fields('private is_private can_write',boolean)})
D['syncJob'] = obj(fields('job_id project_id release_id status error_code'), ['job_id','project_id','status'])
D['assignment'] = obj({**fields('id skill_id scope activation_mode target_type target_id'),'required':boolean(),'priority':integer()}, ['skill_id','scope','activation_mode','target_type','target_id','required','priority'])
D['event'] = obj({**fields('id trace_id event_type skill_id reason_code created_at'), 'score':DECIMAL})
D['settings'] = obj({'telemetry_enabled':boolean(),'telemetry_retention_days':integer(),'semantic_enabled':boolean(),'embedding_provider':string(),'embedding_model':string(),'feedback_issue_creation_enabled':boolean(),'provider_preferences_json':string(),'assignments':array(ref('assignment')),'recent_events':array(ref('event'))}, ['telemetry_enabled','telemetry_retention_days','semantic_enabled','feedback_issue_creation_enabled','assignments','recent_events'])
measurements='surfaced instructions_delivered supporting_file_read resolver_selected resolver_suggested resolver_excluded agent_reported_used agent_reported_skipped legacy_resolver_selected'
D['usageCounts']=obj(fields(measurements,integer),measurements.split())
D['usageReason']=obj({'reason':string(),'count':integer()},['reason','count'])
D['usageVersion']=obj({**fields('release_id version checksum'),'counts':ref('usageCounts')},['counts'])
D['usageSkill']=obj({**fields('skill_id name last_activity'),'is_current':boolean(),'counts':ref('usageCounts'),'versions':array(ref('usageVersion')),'resolver_exclusion_reasons':array(ref('usageReason')),'agent_skip_reasons':array(ref('usageReason'))},['skill_id','name','is_current','counts','versions','resolver_exclusion_reasons','agent_skip_reasons'])
D['usage']=obj({'collection_enabled':boolean(),'retention_days':integer(),**fields('requested_window effective_from effective_to reporting_coverage historical_measurements'),**fields('page page_size total',integer),'skills':array(ref('usageSkill'))},['collection_enabled','retention_days','requested_window','effective_from','effective_to','page','page_size','total','skills'])
D['catalogTool']=obj(fields('name description input_schema_json'),['name','description','input_schema_json'])
D['resource']=obj({**fields('name title uri description mimeType mime_type'),'size':integer()},['name','uri'])
D['promptArgument']=obj({'name':string(),'description':string(),'required':boolean()},['name'])
D['prompt']=obj({**fields('name title description'),'arguments':array(ref('promptArgument'))},['name'])
D['catalog']=obj({**fields('release_id release_status mcp_url catalog_markdown catalog_markdown_generated catalog_markdown_override'),'mcp_oauth_enabled':boolean(),'tools':array(ref('catalogTool')),'resources':array(ref('resource')),'prompts':array(ref('prompt'))},['catalog_markdown','catalog_markdown_generated','tools','resources','prompts'])
D['domainRecord']=obj(fields('type name value status purpose'),['type','name','value','status','purpose'])
D['domain']=obj({**fields('hostname verification_token verification_record_name ownership_verification_record_name ownership_verification_record_value instructions certificate_status certificate_message'),'verified':boolean(),'platform_dns_records':array(ref('domainRecord'))},['verified','certificate_status','platform_dns_records'])
D['methodCount']=obj({'method':string(),'count':integer()},['method','count'])
D['projectTraffic']=obj({'project_id':string(),'project_name':string(),'request_count':integer()},['project_id','project_name','request_count'])
D['capabilityUsage']=obj({**fields('kind key'),**fields('invocations_last_7d successful_last_7d',integer)},['kind','key','invocations_last_7d','successful_last_7d'])
D['summary']=obj({**fields('total_requests requests_last_24h requests_last_7d metrics_sample_size_last_7d projects_total projects_with_active_release active_tools_total active_resources_total active_prompts_total active_tools active_resources active_prompts',integer),**{k:DECIMAL for k in 'success_rate_last_7d avg_latency_ms_last_7d'.split()},'p95_latency_ms_last_7d':integer(),**fields('project_id active_release_id active_commit_sha active_release_status'),'method_breakdown_last_7d':array(ref('methodCount')),'top_projects_last_7d':array(ref('projectTraffic')),'capability_usage_last_7d':array(ref('capabilityUsage'))},['total_requests','requests_last_24h','requests_last_7d','metrics_sample_size_last_7d','method_breakdown_last_7d'])
D['bucket']=obj({**fields('label start end'),**fields('request_count success_count',integer),'avg_latency_ms':DECIMAL},['label','start','end','request_count','success_count'])
D['timeseries']=obj({**fields('range_key range_start range_end project_id rollup_updated_at data_source_note'),'buckets':array(ref('bucket'))},['range_key','range_start','range_end','buckets'])
D['privilegedAccount']=obj({**fields('account_id github_login admin_granted_at paywall_bypass_granted_at'),**fields('is_admin paywall_bypass',boolean)},['account_id','github_login','is_admin','paywall_bypass'])
D['account']=obj({**fields('id login avatar_url email atproto_did atproto_handle plan app_env session_id'),**fields('github_id',integer),**fields('is_admin paywall_bypass github_app_configured github_app_install_suggested github_connected atproto_enabled internal_pro_bypass can_manage_subscription non_production_bypasses needs_project_selection suggested_github_app_install',boolean)},['id','login','is_admin'])
D['source']=obj(fields('repository path revision checksum'),['path','checksum'])
D['skillFile']=obj({**fields('path checksum mediaType resourceUri'),'byteCount':integer()},['path','checksum','mediaType','resourceUri','byteCount'])
D['skillDocument']=obj({'schemaVersion':integer(),**fields('kind id name description instructions version releaseId checksum mediaType resourceUri path text blob encoding'),'byteCount':integer(),'source':ref('source'),'files':array(ref('skillFile'))},['schemaVersion','kind','id','version','checksum','mediaType','resourceUri','source'])
D['context']=obj(fields('user organization workspace repository task'))
D['availableTool']=obj(fields('server name description inputSchema provider'),['server','name'])
D['binding']=obj({**fields('skillId capability selectedServer selectedTool fallback reason'),'required':boolean(),'confidence':DECIMAL,'alternatives':strings(),'missing':boolean()},['capability','required','confidence','alternatives','missing','fallback','reason'])
D['resolvedSkill']=obj({**fields('id version releaseId kind scope enforcement selectionReason instructions resourceUri'),'priority':integer(),'score':DECIMAL,'contentIncluded':boolean(),'source':ref('source')},['id','version','releaseId','kind','scope','enforcement','priority','selectionReason','score','contentIncluded','resourceUri','source'])
D['conflict']=obj({'skillId':string(),'conflictsWith':string(),'unresolved':boolean()},['skillId','conflictsWith','unresolved'])
D['nextAction']=obj({'order':integer(),**fields('type skillId capability instruction resourceUri')},['order','type','instruction'])
D['traceStep']=obj({**fields('skillId outcome reason'),'score':DECIMAL},['outcome','reason'])
D['resolution']=obj({'schemaVersion':integer(),'traceId':string(),'activeSkills':array(ref('resolvedSkill')),'suggestedTaskSkills':array(ref('resolvedSkill')),'capabilityBindings':array(ref('binding')),'missingRequirements':strings(),'conflicts':array(ref('conflict')),'missingContext':strings(),'eventCanonical':boolean(),'nextActions':array(ref('nextAction')),'resolutionTrace':array(ref('traceStep'))},['schemaVersion','traceId','activeSkills','suggestedTaskSkills','capabilityBindings','missingRequirements','conflicts','missingContext','nextActions','resolutionTrace'])
D['toolAnnotations']=obj({'title':string(),**fields('readOnlyHint destructiveHint idempotentHint openWorldHint',boolean)})
D['tool']=obj({**fields('name title description input_schema_json output_schema_json'),'annotations':ref('toolAnnotations')},['name','input_schema_json'])
D['textContent']=obj({'type':string(),'text':string()},['type','text'])
D['resourceContent']=obj({**fields('uri mimeType text blob')},['uri'])
D['message']=obj({'role':string(),'content':ref('textContent')},['role','content'])
D['usageItem']=obj({**fields('skill_id version release_id checksum outcome skip_reason')},['skill_id','version','outcome'])

P={'project_id':string()}
R={**P,'release_id':string()}
S={**R,'skill_id':string()}
RESOLVE={'request':string(maxLength=65536),'event':string(),'context':ref('context'),**fields('user organization workspace repository task'),'current_skill_ids':strings(),'available_tools':array(ref('availableTool'))}
OPS={}
def op(name, inputs=None, output=None, required=(), envelope=None): OPS[PREFIX+name]=(inputs or {},output,tuple(required),envelope)
op('project.listProjects',output=obj({'projects':array(ref('project'))},['projects']),envelope='projects')
op('project.createProject',{'name':string(minLength=1,maxLength=256),'slug':string(minLength=1,maxLength=128)},ref('project'),['name','slug'])
op('project.getProject',P,ref('project'),P)
op('project.updateProject',{**P,'name':string(minLength=1,maxLength=256)},ref('project'),['project_id','name'])
op('project.setActive',P,ref('project'),P)
op('security.listKeys',{**P,'include_revoked':boolean()},obj({'keys':array(ref('apiKey'))},['keys']),P,'keys')
op('security.createKey',{**P,'name':string(maxLength=64)},ref('createdKey'),P)
op('security.updateKey',{**P,'key_id':string(),'name':string(maxLength=64)},ref('apiKey'),['project_id','key_id','name'])
op('security.revokeKey',{**P,'key_id':string()},required=['project_id','key_id'])
op('release.listReleases',P,obj({'releases':array(ref('release'))},['releases']),P,'releases')
op('release.activateRelease',R,required=R)
op('release.getValidation',R,ref('validation'),R)
op('release.listCompiledSkills',R,obj({'skills':array(ref('compiledSkill'))},['skills']),R,'skills')
op('release.updateCompiledSkill',{**S,**fields('exposure_type risk_level status summary skill_body schema_json'),'replace_schema':boolean(),'routing':ref('routing'),'runtime':ref('runtimePatch')},ref('compiledSkill'),S)
op('release.proposeMetadataChange',S,ref('proposal'),S)
op('analytics.listRequestLogs',{**P,'limit':integer(minimum=1),'offset':integer(minimum=0)},obj({'logs':array(ref('requestLog'))},['logs']),P,'logs')
op('repository.connectRepository',{**P,**fields('provider owner repo branch repo_owner repo_name default_branch auth_type token installation_id repository_did owner_did'), 'is_private':boolean(), 'migrate_from':obj(fields('provider repo_owner repo_name default_branch'), ['provider','repo_owner','repo_name','default_branch'])},ref('repositoryConnection'),P)
op('repository.getConnection',P,ref('repositoryConnection'),P)
op('telemetry.getUsage',{**P,**fields('window sort direction skill_id'),'page':integer(minimum=1,maximum=1000000),'page_size':integer(minimum=1,maximum=100)},ref('usage'),P)
op('runtime.getSettings',P,ref('settings'),P)
op('runtime.updateSettings',{**P,'telemetry_enabled':boolean(),'telemetry_retention_days':integer(),'feedback_issue_creation_enabled':boolean(),'provider_preferences_json':string(),'assignments':array(ref('assignment'),maxLength=100)},ref('settings'),P)
op('catalog.getCatalog',P,ref('catalog'),P)
op('catalog.updateMarkdown',{**P,'markdown':string(maxLength=524288)},obj(fields('catalog_markdown catalog_markdown_generated catalog_markdown_override'),['catalog_markdown','catalog_markdown_generated']),['project_id','markdown'])
op('repository.syncRepository',P,ref('syncJob'),P)
for name in ['getDomain','verifyDomain']:op('domain.'+name,P,ref('domain'),P)
op('domain.setDomain',{**P,'hostname':string()},ref('domain'),['project_id','hostname'])
for name in ['getAccountSummary','getProjectSummary']:op('analytics.'+name,P if 'Project' in name else {},ref('summary'),P if 'Project' in name else ())
for name in ['getAccountTimeseries','getProjectTimeseries']:op('analytics.'+name,{**(P if 'Project' in name else {}),'range':string()},ref('timeseries'),P if 'Project' in name else ())
op('admin.getMetrics',output=obj(fields('total_users total_projects total_mcp_calls',integer),['total_users','total_projects','total_mcp_calls']))
op('admin.listPrivilegedAccounts',output=obj({'accounts':array(ref('privilegedAccount'))},['accounts']),envelope='accounts')
for name in ['lookupAccount','updateAccountFlags']:
    op('admin.'+name,fields('github_login github_id email') if name=='lookupAccount' else {'account_id':string(),'is_admin':boolean()},obj({'account_id':string(),'is_admin':boolean(),'paywall_bypass':boolean()},['account_id','is_admin','paywall_bypass']),() if name=='lookupAccount' else ['account_id','is_admin'])
op('admin.getTimeseries',{'range':string()},ref('timeseries'))
op('admin.refreshRollups')
op('account.getAccount',output=ref('account'))
op('account.logout', {'session_id':string()}, required=['session_id'])
op('repository.listRepositories',{'provider':string(),'page':integer(minimum=1)},obj({'repositories':array(ref('repository'))},['repositories']),envelope='repositories')
op('repository.getSyncJob',{'job_id':string()},ref('syncJob'),['job_id'])
op('agent.resolveContext',RESOLVE,ref('resolution'),['request'])
op('agent.discoverSkills',{**RESOLVE,**fields('query skill_id')},ref('resolution'))
op('agent.listCapabilities',{**RESOLVE,'skill_id':string()},obj({'schemaVersion':integer(),'knownCapabilities':strings(),'bindings':array(ref('binding')),'unresolvedRequirements':strings()},['schemaVersion','knownCapabilities','bindings','unresolvedRequirements']))
op('agent.getCatalog',{**RESOLVE,**fields('mode skill task')},obj({'resolution':ref('resolution'),'skill':ref('skillDocument'),'markdown':string()}))
op('agent.getSkill',fields('skill_id version path'),ref('skillDocument'),['skill_id'])
op('agent.reportFeedback',{**fields('skill_id version category'),'summary':string(maxLength=2000),'evidence':string(maxLength=8000),'suggested_change':string(maxLength=8000),'create_issue':boolean()},obj({'schemaVersion':integer(),'feedbackId':string(),'effectStatus':string(),'issueDraft':obj(fields('title body'),['title','body']),'creationAuthorized':boolean(),'message':string()},['schemaVersion','feedbackId','effectStatus','issueDraft','creationAuthorized','message']),['skill_id','version','category','summary','evidence'])
op('agent.reportUsage',{'report_id':string(minLength=1,maxLength=128),'trace_id':string(),'skills':array(ref('usageItem'),minLength=1,maxLength=100)},obj({'status':string(),'recorded_count':integer(minimum=0)},['status','recorded_count']),['report_id','skills'])
for name,item,key in [('listTools','tool','tools'),('listResources','resource','resources'),('listPrompts','prompt','prompts')]:op('agent.'+name,{'cursor':string()},obj({key:array(ref(item)),'nextCursor':string()},[key]))
op('agent.readResource',{'uri':string()},obj({'contents':array(ref('resourceContent'))},['contents']),['uri'])
op('agent.getPrompt',{'name':string(),'arguments':array(obj({'name':string(),'value':string()},['name','value']))},obj({'description':string(),'messages':array(ref('message'))},['messages']),['name'])
for name in ['subscribeResource','unsubscribeResource']:op('agent.'+name,{'uri':string()},obj({}),['uri'])

op('publication.getReview',{**P,'cursor':integer(minimum=0),'limit':integer(minimum=1,maximum=100)},obj({'owner_did':string(),'project_id':string(),'digest':string(),'entry_count':integer(),'has_telemetry':boolean(),'warnings':strings(),'entries_json':string(),'next_cursor':integer()},['owner_did','project_id','digest','entry_count','has_telemetry','warnings','entries_json']),P)
op('publication.publishReview',{**P,'review_digest':string(),'public':boolean(),'telemetry':boolean()},obj({'status':string(),'digest':string(),'completed':integer(),'total':integer(),'uri':string(),'cid':string()},['status','digest']),['project_id','review_digest','public','telemetry'])
op('publication.reconcileProject',P,obj({'status':string()},['status']),P)
op('publication.verifyProject',P,obj({**fields('status owner_did project_id'),'checked_records':integer(minimum=0),'collections':array(obj({'collection':string(),'checked_records':integer(minimum=0)},['collection','checked_records'])),'issues':array(obj(fields('code message uri expected_cid actual_cid'),['code','message']))},['status','owner_did','project_id','checked_records','collections','issues']),P)

op('repository.listTangledRepositories',fields('cursor'),output=obj({'provider':string(),'repositories':array(obj(fields('repository_did owner_did owner_handle name default_branch uri cid'),['repository_did','owner_did','name','default_branch','uri','cid'])),'cursor':string(),'private_repository_selection':string()},['provider','repositories','private_repository_selection']))

op('publication.getConflict',P,obj(fields('status intent_id source_cid target_digest current_manifest_cid current_digest'),['status']),P)
op('publication.resolveConflict',{**P,**fields('intent_id expected_current_cid action')},obj(fields('status'),['status']),['project_id','intent_id','expected_current_cid','action'])

ERRORS=[{'name':name} for name in ['InvalidRequest','AuthenticationRequired','Forbidden','NotFound','Conflict','RateLimitExceeded','ServiceUnavailable']]

def generate():
    manifest=json.loads((ROOT/'lexicons/operations.json').read_text())
    schemas={DEFS:{'lexicon':1,'id':DEFS,'description':'Shared typed views. Decimal metrics are base-10 strings. Optional null SQL values are omitted.','defs':D}}
    metadata=[]
    for item in manifest['operations']:
        name=item['nsid']; assert name in OPS,name
        inputs,output,required,envelope=OPS[name]
        query=item['method']=='GET'
        main={'type':'query' if query else 'procedure','description':f"{item['service']} operation {item['path']}. Authorization and resource ownership are required.",'errors':ERRORS}
        schema=obj(inputs,required)
        if query:
            schema['type']='params';main['parameters']=schema
        else:main['input']={'encoding':'application/json','schema':schema}
        if output is not None:main['output']={'encoding':'application/json','schema':output}
        schemas[name]={'lexicon':1,'id':name,'defs':{'main':main}}
        item['contract']='draft'
        if item['service'] in ('control','identity','githubapp','sync','mcp'):item['runtime']='implemented'
        metadata.append({'nsid':name,'service':item['service'],'method':'GET' if query else 'POST','restMethod':item['method'],'restPath':item['path'],'admin':item['authorization']=='admin','noOutput':output is None,'envelope':envelope or '', 'input':schema,'output':output})
    # Permission sets stay within their namespace group and inherit the caller's
    # deployment audience. Administrator grants remain a separate opt-in bundle.
    for group in sorted({item['nsid'].rsplit('.', 1)[0] for item in metadata}):
        for admin in (False, True):
            methods = [item['nsid'] for item in metadata if item['nsid'].rsplit('.', 1)[0] == group and item['admin'] == admin]
            if not methods: continue
            name = group + ('.authAdmin' if admin else '.authClient')
            schemas[name] = {'lexicon':1,'id':name,'defs':{'main':{'type':'permission-set','title':('Admin ' if admin else '') + group.split('.')[-1].capitalize() + ' access','detail':'Call the published operations for this capability. Account authorization, ownership and administrator restrictions still apply.','permissions':[{'type':'permission','resource':'rpc','inheritAud':True,'lxm':methods}]}}}
    record_types = sorted(path.stem for path in (ROOT/'lexicons').glob('dev.mycontextprotocol.data.*.json') if json.loads(path.read_text()).get('defs',{}).get('main',{}).get('type') == 'record')
    name = 'dev.mycontextprotocol.data.authRecords'
    schemas[name] = {'lexicon':1,'id':name,'defs':{'main':{'type':'permission-set','title':'Public project records','detail':'Create, update and delete public project metadata, releases, feedback and opted-in telemetry. Public copies may be replicated. Skill files remain in Git.','permissions':[{'type':'permission','resource':'repo','collection':record_types,'action':['create','update','delete']}]}}}
    return manifest,schemas,metadata

def ts_type(schema):
    typ=schema['type']
    if typ=='ref':return 'XRPC'+schema['ref'].split('#')[-1][0].upper()+schema['ref'].split('#')[-1][1:]
    if typ=='string':return 'string'
    if typ=='integer':return 'number'
    if typ=='boolean':return 'boolean'
    if typ=='array':return 'Array<'+ts_type(schema['items'])+'>'
    if typ in ('object','params'):
        required=schema.get('required',[])
        return '{ '+ '; '.join(json.dumps(k)+('' if k in required else '?')+': '+ts_type(v) for k,v in schema['properties'].items())+' }'
    raise ValueError(typ)

def artifacts():
    manifest,schemas,metadata=generate()
    out={ROOT/'lexicons/operations.json':json.dumps(manifest,indent=2)+'\n'}
    for nsid,schema in schemas.items():out[ROOT/'lexicons'/f'{nsid}.json']=json.dumps(schema,indent=2)+'\n'
    out[ROOT/'services/go/xrpc/contracts.json']=json.dumps({'schemas':schemas,'operations':metadata},indent=2)+'\n'
    ts=['// Generated by scripts/generate-xrpc.py. Do not edit.','']
    for name,schema in D.items():ts.append('export type XRPC'+name[0].upper()+name[1:]+' = '+ts_type(schema)+';')
    ts+=['','export interface XRPCMethods {']
    for item in metadata:ts.append('  '+json.dumps(item['nsid'])+': { input: '+ts_type(item['input'])+'; output: '+(ts_type(item['output']) if item['output'] else 'void')+'; method: '+json.dumps(item['method'])+' };')
    ts+=['}', '', 'export const XRPC_HTTP_METHODS = {']
    for item in metadata:ts.append('  '+json.dumps(item['nsid'])+': '+json.dumps(item['method'])+',')
    ts+=['} as const;', '', 'export const XRPC_NO_OUTPUT = {']
    for item in metadata:ts.append('  '+json.dumps(item['nsid'])+': '+str(item['output'] is None).lower()+',')
    ts+=['} as const;', '', 'export const XRPC_SERVICES = {']
    for item in metadata:ts.append('  '+json.dumps(item['nsid'])+': '+json.dumps(item['service'])+',')
    ts+=['} as const;', '']
    out[ROOT/'packages/mycontext-web-client/src/xrpc-types.ts']='\n'.join(ts)
    return out

def main():
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--check',action='store_true');args=parser.parse_args()
    for path,content in artifacts().items():
        if args.check:
            if not path.exists() or path.read_text()!=content:raise SystemExit(f'Generated XRPC artifact is stale: {path.relative_to(ROOT)}')
        else:path.parent.mkdir(parents=True,exist_ok=True);path.write_text(content)
if __name__=='__main__':main()
