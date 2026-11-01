$ErrorActionPreference='Continue'
$root=(Get-Location).Path
$r=Join-Path $root 'verification/stage2c1-fix2'
$sha=git rev-parse HEAD
# Regenerate overlay paths so this evidence can be rerun from another worktree.
$virtual=Join-Path $root 'server/internal/logic/rag/independent_config_review_test.go'
$source=Join-Path $r 'independent-original_test.go.txt'
$mapping=@{};$mapping[$virtual]=$source
$numeric=Join-Path $root 'server/internal/logic/rag/independent_numeric_review_test.go'
$mapping[$numeric]=Join-Path $r 'independent-numeric_test.go.txt'
@{Replace=$mapping}|ConvertTo-Json -Depth 4|Set-Content "$r/overlay.json" -Encoding utf8
$env:PATH='C:\msys64\ucrt64\bin;'+$env:PATH
$env:CC='C:\msys64\ucrt64\bin\gcc.exe';$env:CXX='C:\msys64\ucrt64\bin\g++.exe';$env:CGO_ENABLED='1';$env:GOFLAGS='';$env:GOPROXY='off';$env:GOSUMDB='off'
Set-Location server
@("CODE_SHA: $sha",'BASELINE: 8611750bb89dbf0de268b04a212ca873d500cb6f',('CWD: '+(Get-Location).Path),'Process only; GOFLAGS empty; GOPROXY=off; GOSUMDB=off')|Set-Content "$r/environment.log"
go version|Add-Content "$r/environment.log"
go env GOOS GOARCH CGO_ENABLED CC CXX GOFLAGS|Add-Content "$r/environment.log"
gcc --version|Select-Object -First 1|Add-Content "$r/environment.log"
$pkgs=@('./core/config','./core/common','./core/vector','./core/indexer','./core/retriever','./core','./internal/logic/rag')
$commands=@(
 ,(@('focused','test')+$pkgs+@('-run','^TestDimensions_','-count=1','-timeout=180s','-v'))
 ,(@('affected','test')+$pkgs+@('-count=1','-timeout=180s'))
 ,(@('race-affected','test','-race')+$pkgs+@('-count=1','-timeout=180s','-v'))
 ,@('independent-original','test','-overlay',"$r/overlay.json",'./internal/logic/rag','-run','^TestIndependent','-count=1','-timeout=120s','-v')
 ,@('full','test','-p','1','./...','-count=1','-timeout=180s')
 ,@('vet','vet','./...')
 ,@('build','build','./...')
)
foreach($c in $commands){$n=$c[0];$a=$c[1..($c.Length-1)];$log="$r/$n.log";@("CODE_SHA: $sha",('CWD: '+(Get-Location).Path),('CMD: go '+($a -join ' ')),('DATE: '+(Get-Date -Format o)))|Set-Content $log;go @a 2>&1|Out-File $log -Append -Encoding utf8;$rc=$LASTEXITCODE;"EXIT: $rc"|Add-Content $log;Write-Output "$n EXIT $rc";if($rc -ne 0){exit $rc}}



