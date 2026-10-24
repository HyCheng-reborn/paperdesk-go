$ErrorActionPreference='Continue'
Set-Location C:/Zhy/code/go-rag-review-1d3/server
$dest='C:/Zhy/code/go-rag/verification/independent-1d3-71c4c81'
$env:GOPROXY='off';$env:GOSUMDB='off'
$commands=@(
'test ./core ./internal/logic/knowledge ./internal/logic/rag -count=1 -v -timeout=90s -run ^(TestIndex_Background(Chunks.*|CleanupBeforeDone)|TestSaveChunks_.*|TestReset_.*|TestShutdownCoordinator_.*)$',
'test ./core ./internal/logic/rag/... ./internal/logic/knowledge/... ./internal/cmd/... -count=1 -timeout=90s',
'test -race ./core ./internal/logic/rag/... ./internal/logic/knowledge/... ./internal/cmd/... -count=1 -timeout=90s',
'test -race ./core ./internal/logic/knowledge -count=10 -v -timeout=120s -run ^(TestIndex_Background(Chunks.*|CleanupBeforeDone)|TestSaveChunks_IndexingOnlyUpdater_SkipsFailed|TestShutdownCoordinator_.*)$',
'test -p 1 ./... -count=1 -timeout=90s',
'build ./...',
'vet ./...'
)
$i=0
foreach($command in $commands){$i++;$log=$dest+'/target-'+$i+'.log';@('SHA: 71c4c81cb6a3eae060365e1f59f6dfe79aed83ce','LAYER: clean target',('CWD: '+(Get-Location)),('CMD: go '+$command),'GOPROXY=off GOSUMDB=off',('START: '+(Get-Date -Format o)))|Set-Content $log;& go @($command.Split(' ')) *>> $log;$code=$LASTEXITCODE;('EXIT: '+$code)|Add-Content $log;Write-Output ($i.ToString()+' EXIT '+$code)}
