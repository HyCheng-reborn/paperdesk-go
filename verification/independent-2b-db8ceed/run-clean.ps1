$ErrorActionPreference='Continue'
$r='C:/Zhy/code/go-rag/verification/independent-2b-db8ceed'
Set-Location C:/Zhy/code/go-rag-review-1d3/server
$env:PATH='C:\msys64\ucrt64\bin;'+$env:PATH
$env:CC='C:\msys64\ucrt64\bin\gcc.exe';$env:CXX='C:\msys64\ucrt64\bin\g++.exe';$env:CGO_ENABLED='1';$env:GOFLAGS='';$env:GOPROXY='off';$env:GOSUMDB='off'
@('SHA: db8ceede9bb0e7475a3bb5b5a82e853063da5d49',('CWD: '+(Get-Location).Path),'ENV: process only; GOPROXY=off GOSUMDB=off GOFLAGS empty')|Set-Content "$r/environment.log"
go version|Add-Content "$r/environment.log"
go env GOOS GOARCH CGO_ENABLED CC CXX GOFLAGS|Add-Content "$r/environment.log"
gcc --version|Add-Content "$r/environment.log"
ld --version|Select-Object -First 1|Add-Content "$r/environment.log"
$regex='^(TestBudget_(RealHTTPSend|StreamFinish|RetryNoRefresh|ConcurrentReserve_MaxRequests|ConcurrentFinalize)|TestBuilder_(ConcurrentRegistration|UnknownProviderRejected|NilBuilderRejected|DuplicateRegistrationRejected|FrozenAfterRegistryBuild)|TestEmbedding_(RawIndices_(OutOfOrder|Duplicate|Missing|Valid)|UsageMissing)|TestRegistry_GetSetConcurrency|TestStream_(EarlyCloseFinalizes|CancelFinalizes|ErrorFinalizes|ConcurrentCloseFinalizes|MultiChunkComplete|BlockedSourceClose|ConcurrentClose|CancelNotEOF)|TestBudget_(RedirectCountsAttempts|OutputCapMatches|EmbeddingSettlesUsage|LocalErrorNoCommit|OwnerReuseViaCtx|OutputOptsMatchReservation|OutputOptsRejected)|TestOpenAI_(BudgetCoversHTTP|StreamBudget)|TestOwner_.*|TestRedirect_.*|TestEmbedding_ConcurrentUsageIsolated)$'
$commands=@(
 @('ordinary','test','./core/providers/...','./core/common','./core/rerank','./core/config','./internal/logic/rag','./internal/logic/chat','-count=1','-timeout=180s','-v'),
 @('race-providers','test','-race','./core/providers/...','-count=1','-timeout=180s','-v'),
 @('race-wiring','test','-race','./internal/controller/rag','./core/common','./core/rerank','./internal/logic/rag','./internal/logic/chat','-count=1','-timeout=180s','-v'),
 @('race-key-stress','test','-race','./core/providers/...','-run',$regex,'-count=10','-timeout=180s','-v'),
 @('full','test','-p','1','./...','-count=1','-timeout=180s'),
 @('vet','vet','./...'),
 @('build','build','./...')
)
foreach($c in $commands){$name=$c[0];$a=$c[1..($c.Length-1)];$log="$r/$name.log";@('SHA: db8ceede9bb0e7475a3bb5b5a82e853063da5d49',('CWD: '+(Get-Location).Path),('CMD: go '+($a -join ' ')),('DATE: '+(Get-Date -Format o)))|Set-Content $log;go @a 2>&1|Out-File $log -Append -Encoding utf8;$result=$LASTEXITCODE;"EXIT: $result"|Add-Content $log;Write-Output "$name EXIT $result"}







