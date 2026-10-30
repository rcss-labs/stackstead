package apitypes_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// populated returns a pointer to a zero value of T with every field set to a
// non-zero value, so omitempty tags cannot hide a key.
func populated[T any]() T {
	var v T
	fill(reflect.ValueOf(&v).Elem())
	return v
}

func fill(v reflect.Value) {
	if !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint64:
		v.SetUint(1)
	case reflect.Slice:
		if v.Type() == reflect.TypeOf(json.RawMessage{}) {
			v.SetBytes([]byte(`{"x":1}`))
			return
		}
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Float64:
		v.SetFloat(1)
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Unix(1, 0).UTC()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			fill(v.Field(i))
		}
	}
}

func TestJSONTagsMatchContract(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"StorageConsumer", populated[apitypes.StorageConsumer](), "containerId,name,project,service,stack,state"},
		{"StorageDeclaration", populated[apitypes.StorageDeclaration](), "ref,service,stack,stackState"},
		{"StorageProtection", populated[apitypes.StorageProtection](), "reason,stack"},
		{"StorageImage", populated[apitypes.StorageImage](), "containers,created,dangling,declaredBy,digests,id,sharedSize,size,status,tags"},
		{"StorageVolume", populated[apitypes.StorageVolume](), "containers,createdAt,declaredBy,driver,labels,name,project,protection,refCount,scope,size,sizesPending,status"},
		{"StorageBuildRecord", populated[apitypes.StorageBuildRecord](), "createdAt,description,id,inUse,lastUsedAt,shared,size,type,usageCount"},
		{"StorageKindTotals", populated[apitypes.StorageKindTotals](), "count,error,reclaimable,refreshing,size,sizesAt"},
		{"StorageTopContainer", populated[apitypes.StorageTopContainer](), "name,sizeRw,stack"},
		{"StorageOverview", populated[apitypes.StorageOverview](), "buildCache,containers,declarationsUnknown,images,topContainers,volumes"},
		{"StorageImages", populated[apitypes.StorageImages](), "images,totals"},
		{"StorageVolumes", populated[apitypes.StorageVolumes](), "totals,volumes"},
		{"StorageBuildCache", populated[apitypes.StorageBuildCache](), "records,totals"},
		{"StoragePlanRequest", populated[apitypes.StoragePlanRequest](), "kind,name,ref,selection"},
		{"StoragePlanItem", populated[apitypes.StoragePlanItem](), "bytes,id,kind,ref"},
		{"StorageProtectedItem", populated[apitypes.StorageProtectedItem](), "id,kind,reason,ref,stack,stackState"},
		{"StorageReclaim", populated[apitypes.StorageReclaim](), "exact,max,min"},
		{"StoragePlan", populated[apitypes.StoragePlan](), "confirmPhrase,createdAt,expiresAt,id,items,kind,mode,protected,reclaim,warnings"},
		{"StorageConfirmRequest", populated[apitypes.StorageConfirmRequest](), "confirm"},
		{"StorageRefresh", populated[apitypes.StorageRefresh](), "startedAt"},
		{"StorageRateLimit", populated[apitypes.StorageRateLimit](), "error,retryAfter"},
		{"ActionEntry", populated[apitypes.ActionEntry](), "bytes,detail,id,kind,operationId,result,scope,target,ts,username"},
		{"ContainerMount", populated[apitypes.ContainerMount](), "destination,name,readOnly,source,type"},
		{"StorageImageDetail", populated[apitypes.StorageImageDetail](), "architecture,containers,created,dangling,declaredBy,digests,id,labels,layers,os,sharedSize,size,status,tags"},
		{"StorageVolumeDetail", populated[apitypes.StorageVolumeDetail](), "containers,createdAt,declaredBy,driver,labels,mountpoint,name,project,protection,refCount,scope,size,sizesPending,status"},
		{"StackSummary", populated[apitypes.StackSummary](), "health,libraryUpdates,name,origin,path,ports,project,running,serviceCount,status,warnings"},
		{"StackDetail", populated[apitypes.StackDetail](), "hash,health,name,origin,parseError,path,ports,project,running,serviceCount,services,status"},
		{"ServiceDetail", populated[apitypes.ServiceDetail](), "config,containers,library,name,readOnly"},
		{"ServiceConfig", populated[apitypes.ServiceConfig](), "build,environment,healthcheck,image,limits,networkMode,networks,ports,restart,volumes"},
		{"ContainerInfo", populated[apitypes.ContainerInfo](), "health,id,name,service,state,status"},
		{"Build", populated[apitypes.Build](), "context,dockerfile"},
		{"EnvVar", populated[apitypes.EnvVar](), "name,value"},
		{"NetworkAttachment", populated[apitypes.NetworkAttachment](), "ipv4Address,name"},
		{"Limits", populated[apitypes.Limits](), "cpus,memory"},
		{"Healthcheck", populated[apitypes.Healthcheck](), "interval,retries,startPeriod,test,timeout"},
		{"Operation", populated[apitypes.Operation](), "action,endedAt,error,exitCode,id,service,stack,startedAt,state"},
		{"OperationDetail", populated[apitypes.OperationDetail](), "action,endedAt,error,exitCode,id,lines,service,stack,startedAt,state"},
		{"NetworkCreateRequest", populated[apitypes.NetworkCreateRequest](), "driver,gateway,internal,ipRange,ipvlanMode,name,parent,subnet"},
		{"NetworkContainer", populated[apitypes.NetworkContainer](), "exitCode,health,id,ipv4,mac,name,stack,state"},
		{"Network", populated[apitypes.Network](), "attachable,builtin,containers,created,driver,fullId,gateways,id,internal,ipRange,ipv6,ipvlanMode,name,parent,project,scope,stack,subnets"},
		{"FSEntry", populated[apitypes.FSEntry](), "isDir,name,size"},
		{"Status", populated[apitypes.Status](), "arch,compose,docker,dockerVersion,events,metricsDegraded,metricsIntervalSeconds,metricsRetentionSeconds,version"},
		{"ErrorBody", populated[apitypes.ErrorBody](), "code,error,fields"},
		{"FieldError", populated[apitypes.FieldError](), "field,message"},
		{"RawCompose", populated[apitypes.RawCompose](), "content,hash"},
		{"HashResponse", populated[apitypes.HashResponse](), "hash"},
		{"PreviewResponse", populated[apitypes.PreviewResponse](), "content,hash"},
		{"DockerfileContent", populated[apitypes.DockerfileContent](), "content,hash"},
		{"DockerfileView", populated[apitypes.DockerfileView](), "content,editable,exists,hash,path,reason"},
		{"CreateStackRequest", populated[apitypes.CreateStackRequest](), "config,extra,name,service,services"},
		{"CreateStackService", populated[apitypes.CreateStackService](), "config,dockerfile,extra,service"},
		{"AddServiceRequest", populated[apitypes.AddServiceRequest](), "config,dockerfile,hash,service"},
		{"ImportRequest", populated[apitypes.ImportRequest](), "path"},
		{"UpdateServiceRequest", populated[apitypes.UpdateServiceRequest](), "config,dockerfile,hash"},
		{"LogEvent", populated[apitypes.LogEvent](), "container,service,stream,text,time"},
		{"LogError", populated[apitypes.LogError](), "container,error,service"},
		{"MetricPoint", populated[apitypes.MetricPoint](), "blkReadBytesPerSec,blkWriteBytesPerSec,cpu,mem,memLimit,netRxBytesPerSec,netTxBytesPerSec,service,time"},
		{"MetricsHistory", populated[apitypes.MetricsHistory](), "points"},
		{"HealthHistory", populated[apitypes.HealthHistory](), "from,services,to"},
		{"ServiceHealth", populated[apitypes.ServiceHealth](), "restarts,runs,service"},
		{"HealthRun", populated[apitypes.HealthRun](), "from,state,to"},
		{"Sample", populated[apitypes.Sample](), "blkRead,blkWrite,container,cpu,mem,memLimit,netRx,netTx,service,time"},
		{"Change", populated[apitypes.Change](), "project,seq"},
		{"StatusFlags", populated[apitypes.StatusFlags](), "events,metricsDegraded"},
		{"SetupStatus", populated[apitypes.SetupStatus](), "needsSetup"},
		{"SetupRequest", populated[apitypes.SetupRequest](), "password,setupToken,username"},
		{"LoginRequest", populated[apitypes.LoginRequest](), "password,username"},
		{"Me", populated[apitypes.Me](), "username"},
		{"ChangePasswordRequest", populated[apitypes.ChangePasswordRequest](), "current,new"},
		{"MetricsTokenStatus", populated[apitypes.MetricsTokenStatus](), "configured"},
		{"MetricsToken", populated[apitypes.MetricsToken](), "token"},
		{"Health", populated[apitypes.Health](), "ok,peering"},
		{"PeerTrust", populated[apitypes.PeerTrust](), "caPem,pinSha256,plaintext"},
		{"PeerPairRequest", populated[apitypes.PeerPairRequest](), "credential,instanceId,name,protocol,trust,url,version"},
		{"PeerPairResponse", populated[apitypes.PeerPairResponse](), "credential,instanceId,name,protocol,version"},
		{"PeerHello", populated[apitypes.PeerHello](), "features,instanceId,name,protocol,stackCount,url,version"},
		{"PeerUpdate", populated[apitypes.PeerUpdate](), "name,url"},
		{"ServerSelf", populated[apitypes.ServerSelf](), "advertisedUrl,instanceId,name,slug,suggestedUrl,version"},
		{"ServerSelfUpdate", populated[apitypes.ServerSelfUpdate](), "advertisedUrl,name"},
		{"ConnectionToken", populated[apitypes.ConnectionToken](), "expiresAt,token"},
		{"AddPeerRequest", populated[apitypes.AddPeerRequest](), "advertisedUrl,selfTrust,token,trust,url"},
		{"PeerProbeRequest", populated[apitypes.PeerProbeRequest](), "url"},
		{"PeerProbe", populated[apitypes.PeerProbe](), "fingerprintSha256,peering,plaintextAllowed,systemTrusted"},
		{"PeerView", populated[apitypes.PeerView](), "compatible,features,id,lastError,lastSeenAt,name,plaintext,slug,stackCount,status,url,version"},
		{"FleetServer", populated[apitypes.FleetServer](), "error,id,lastSeenAt,local,name,slug,status,version"},
		{"FleetStack", populated[apitypes.FleetStack](), "health,libraryUpdates,name,origin,path,ports,project,running,server,serviceCount,status,warnings"},
		{"FleetEvent", populated[apitypes.FleetEvent](), "action,container,exitCode,health,image,project,server,service,time"},
		{"FleetStacks", populated[apitypes.FleetStacks](), "events,servers,stacks"},
		{"HostSnapshot", populated[apitypes.HostSnapshot](), "containers,cpu,disk,diskIO,hostname,memory,network,os,restarts24h,sampledAt,source,uptime,volumes"},
		{"Alert", populated[apitypes.Alert](), "container,kind,message,service,severity,since,stack"},
		{"MetricsSummary", populated[apitypes.MetricsSummary](), "from,stacks,step,to,top,window"},
		{"ProjectSummary", populated[apitypes.ProjectSummary](), "cpu,cpuNow,mem,memNow,netRxBytesPerSec,netTxBytesPerSec"},
		{"ContainerRow", populated[apitypes.ContainerRow](), "cpu,createdAt,exitCode,health,id,image,imageId,managed,mem,memLimit,name,networks,number,ports,project,restarts24h,service,stack,state,status"},
		{"ContainerDetail", populated[apitypes.ContainerDetail](), "exitCode,finishedAt,health,id,image,imageId,labels,managed,memoryLimit,mounts,name,networks,ports,project,restartPolicy,restarts24h,service,stack,startedAt,state,status"},
		{"TopEntry", populated[apitypes.TopEntry](), "container,project,service,value"},
		{"BackupSchedule", populated[apitypes.BackupSchedule](), "enabled,frequency,time,weekday"},
		{"BackupLocal", populated[apitypes.BackupLocal](), "enabled,keepLast,path"},
		{"BackupS3", populated[apitypes.BackupS3](), "accessKeyId,bucket,enabled,endpoint,pathStyle,prefix,region,secretKey,secretKeySet"},
		{"BackupConfig", populated[apitypes.BackupConfig](), "encryption,local,nextRun,pausedAfterRestore,s3,schedule"},
		{"BackupEncryption", populated[apitypes.BackupEncryption](), "enabled,keyId,passphrase,passphraseSet"},
		{"BackupMount", populated[apitypes.BackupMount](), "bytes,kind,name,target"},
		{"BackupStackSettings", populated[apitypes.BackupStackSettings](), "images,stopDuring,volumes"},
		{"BackupStack", populated[apitypes.BackupStack](), "images,mounts,stack,stopDuring,volumes"},
		{"BackupItem", populated[apitypes.BackupItem](), "bytes,kind,name,note,stack,status"},
		{"BackupRun", populated[apitypes.BackupRun](), "archive,destinations,endedAt,error,id,items,size,startedAt,status,trigger"},
		{"BackupArchive", populated[apitypes.BackupArchive](), "createdAt,dest,encrypted,hostSlug,name,size"},
		{"HostRestoreS3", populated[apitypes.HostRestoreS3](), "accessKeyId,bucket,endpoint,key,pathStyle,region,secretKey"},
		{"HostRestoreSource", populated[apitypes.HostRestoreSource](), "kind,path,s3"},
		{"HostRestoreUploaded", populated[apitypes.HostRestoreUploaded](), "source"},
		{"HostRestoreInspectRequest", populated[apitypes.HostRestoreInspectRequest](), "passphrase,setupToken,source"},
		{"HostRestoreStartRequest", populated[apitypes.HostRestoreStartRequest](), "allowUnencrypted,confirmedDigest,passphrase,setupToken,source,startStacks"},
		{"HostRestoreStarted", populated[apitypes.HostRestoreStarted](), "operation,restoreToken"},
		{"HostRestoreConflict", populated[apitypes.HostRestoreConflict](), "action,kind,name"},
		{"HostRestoreSummary", populated[apitypes.HostRestoreSummary](), "conflicts,conflictsDigest,encrypted,foreign,keyId,manifest,risks"},
		{"BackupManifest", populated[apitypes.BackupManifest](), "createdAt,encrypted,format,host,hostSlug,instanceId,items,keyId,networks,stacks,stacksteadVersion,status"},
		{"BackupManifestStack", populated[apitypes.BackupManifestStack](), "composePath,images,liveCopy,name,networks,origin,project,volumes,wasRunning"},
		{"BackupManifestVolume", populated[apitypes.BackupManifestVolume](), "bytes,key,kind,name,target"},
		{"BackupRunStarted", populated[apitypes.BackupRunStarted](), "operation"},
		{"BackupArchiveRef", populated[apitypes.BackupArchiveRef](), "dest,name"},
		{"BackupRestoreRequest", populated[apitypes.BackupRestoreRequest](), "allowUnencrypted,archive,files,images,newName,passphrase,stack,start,volumeKeys,volumes"},
		{"BackupFileDiff", populated[apitypes.BackupFileDiff](), "new,old,path"},
		{"BackupPlannedVolume", populated[apitypes.BackupPlannedVolume](), "bytes,key,kind,name,reason,skip,target"},
		{"BackupRestoreNetwork", populated[apitypes.BackupRestoreNetwork](), "driver,name"},
		{"BackupRestorePlan", populated[apitypes.BackupRestorePlan](), "composePath,crossHost,encrypted,fileDiffs,foreign,images,keyId,missingNetworks,origin,overwrites,project,risks,stack,targetDir,targetName,volumes,warnings"},
		{"BackupRestoreStarted", populated[apitypes.BackupRestoreStarted](), "operation"},
		{"UpdateStatus", populated[apitypes.UpdateStatus](), "checkedAt,command,current,error,installer,latest,notesURL,state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(keysOf(t, tc.v), ",")
			if got != tc.want {
				t.Errorf("keys = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestOperationStatesAndActions(t *testing.T) {
	if got := strings.Join(apitypes.Actions, ","); got != "up,down,restart,pull,build" {
		t.Errorf("Actions = %s", got)
	}
	states := []string{apitypes.OpRunning, apitypes.OpSucceeded, apitypes.OpFailed, apitypes.OpCancelled, apitypes.OpTimedOut}
	if got := strings.Join(states, ","); got != "running,succeeded,failed,cancelled,timed_out" {
		t.Errorf("states = %s", got)
	}
}

const modulePrefix = "github.com/rcss-labs/stackstead/internal/"

func TestNoServerImports(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "list", "-deps", "github.com/rcss-labs/stackstead/internal/apitypes").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, modulePrefix) && pkg != modulePrefix+"apitypes" {
			t.Errorf("apitypes depends on %s", pkg)
		}
	}
}

func TestCreateStackRequestAcceptsExtraAndDockerfile(t *testing.T) {
	body := `{"name":"a","services":[{"service":"web","config":{"limits":{}},"extra":{"labels":{"a":"b"}},"dockerfile":{"content":"FROM x"}}],"extra":{"volumes":{"d":{}}}}`
	var req apitypes.CreateStackRequest
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !req.UsesServices() {
		t.Error("UsesServices() = false, want true")
	}
	if got := req.Services[0].Dockerfile; got == nil || got.Content != "FROM x" {
		t.Errorf("dockerfile = %+v, want content FROM x", got)
	}
	if got := string(req.Services[0].Extra); got != `{"labels":{"a":"b"}}` {
		t.Errorf("service extra = %s", got)
	}
	if got := string(req.Extra); got != `{"volumes":{"d":{}}}` {
		t.Errorf("stack extra = %s", got)
	}
	if err := json.Unmarshal([]byte(`{"name":"a","services":[],"dockerfiles":{}}`), &req); err == nil {
		t.Error("unknown key dockerfiles was accepted")
	}
}
