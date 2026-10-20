$ErrorActionPreference='Continue'
$root=(Get-Location).Path
$r=Join-Path $root 'verification/stage2c1'
$sha=git rev-parse HEAD
$env:PATH='C:\msys64\ucrt64\bin;'+$env:PATH
$env:CC='C:\msys64\ucrt64\bin\gcc.exe';$env:CXX='C:\msys64\ucrt64\bin\g++.exe';$env:CGO_ENABLED='1';$env:GOFLAGS='';$env:GOPROXY='off';$env:GOSUMDB='off'
Set-Location server
@("CODE_SHA: $sha",'BASELINE: 8d2c21225d4627a2668cad788cc254f78f2e04e4',('CWD: '+(Get-Location).Path),'Process only; GOFLAGS empty; GOPROXY=off; GOSUMDB=off')|Set-Content "$r/environment.log"
go version|Add-Content "$r/environment.log"
go env GOOS GOARCH CGO_ENABLED CC CXX GOFLAGS|Add-Content "$r/environment.log"
gcc --version|Select-Object -First 1|Add-Content "$r/environment.log"
$pkgs=@('./core/config','./core/common','./core/vector','./core/indexer','./core/retriever','./core','./internal/logic/rag')
$commands=@(
 ,(@('focused','test')+$pkgs+@('-run','^TestDimensions_','-count=1','-timeout=180s','-v'))
 ,(@('affected','test')+$pkgs+@('-count=1','-timeout=180s'))
 ,(@('race-affected','test','-race')+$pkgs+@('-count=1','-timeout=180s','-v'))
 ,@('full','test','-p','1','./...','-count=1','-timeout=180s')
 ,@('vet','vet','./...')
 ,@('build','build','./...')
)
foreach($c in $commands){$n=$c[0];$a=$c[1..($c.Length-1)];$log="$r/$n.log";@("CODE_SHA: $sha",('CWD: '+(Get-Location).Path),('CMD: go '+($a -join ' ')),('DATE: '+(Get-Date -Format o)))|Set-Content $log;go @a 2>&1|Out-File $log -Append -Encoding utf8;$rc=$LASTEXITCODE;"EXIT: $rc"|Add-Content $log;Write-Output "$n EXIT $rc";if($rc -ne 0){exit $rc}}
