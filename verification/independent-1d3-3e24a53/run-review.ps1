$ErrorActionPreference='Continue'
Set-Location C:/Zhy/code/go-rag-review-1d3/server
$dest='C:/Zhy/code/go-rag/verification/independent-1d3-3e24a53'
$env:GOPROXY='off';$env:GOSUMDB='off'
$commands=@(
'test ./internal/logic/rag -count=1 -v -timeout=60s -run ^TestReset_',
'test -race ./internal/logic/rag -count=1 -v -timeout=60s -run ^TestReset_',
'test -race ./core ./internal/logic/rag ./internal/logic/knowledge ./internal/cmd -count=1 -timeout=90s',
'test -race ./internal/logic/rag -count=10 -v -timeout=180s -run ^TestReset_',
'test -p 1 ./... -count=1 -timeout=90s',
'build ./...',
'vet ./...'
)
$i=0
foreach($command in $commands){$i++;$log=$dest+'/target-'+$i+'.log';@('SHA: 3e24a533f99aaa8578b848ae550dfd2e10292723','LAYER: clean target',('CWD: '+(Get-Location)),('CMD: go '+$command),'GOPROXY=off GOSUMDB=off',('START: '+(Get-Date -Format o)))|Set-Content $log;& go @($command.Split(' ')) *>> $log;$code=$LASTEXITCODE;('EXIT: '+$code)|Add-Content $log;Write-Output ($i.ToString()+' EXIT '+$code)}
