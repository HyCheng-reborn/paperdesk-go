$ErrorActionPreference='Continue'
$r='C:\Zhy\code\go-rag\verification\independent-2c1-1850da1-20261021\executed'
Set-Location 'C:\Zhy\code\go-rag\.review-stage2c1-1850da1\server'
$env:PATH='C:\msys64\ucrt64\bin;'+$env:PATH
$env:CC='C:\msys64\ucrt64\bin\gcc.exe';$env:CXX='C:\msys64\ucrt64\bin\g++.exe';$env:CGO_ENABLED='1';$env:GOFLAGS='';$env:GOPROXY='off';$env:GOSUMDB='off'
@('TARGET: '+(git rev-parse HEAD),'SERVER TREE: '+(git rev-parse HEAD:server),'CWD: '+(Get-Location).Path,'GOFLAGS empty; GOPROXY=off; GOSUMDB=off') | Set-Content "$r/environment.log"
go version | Add-Content "$r/environment.log"
go env GOFLAGS GOOS GOARCH CC CXX CGO_ENABLED | Add-Content "$r/environment.log"
git status --porcelain | Set-Content "$r/clean-before.log"
$p=@('./core/config','./core/common','./core/vector','./core/indexer','./core/retriever','./core','./internal/logic/rag')
$cs=@(,(@('focused','test')+$p+@('-run','^TestDimensions_','-count=1','-v')); ,(@('affected','test')+$p+@('-count=1','-v')); ,(@('race','test','-race')+$p+@('-count=1','-v')); ,@('full','test','-p','1','./...','-count=1'); ,@('vet','vet','./...'); ,@('build','build','./...'))
foreach($c in $cs){$n=$c[0];$a=$c[1..($c.Length-1)]; @('TARGET: '+(git rev-parse HEAD),'CMD: go '+($a -join ' '),'GOFLAGS: '+$env:GOFLAGS) | Set-Content "$r/$n.log"; go @a 2>&1 | Out-File "$r/$n.log" -Append -Encoding utf8; $rc=$LASTEXITCODE; "EXIT: $rc" | Add-Content "$r/$n.log"; Write-Output "$n EXIT $rc"}
git status --porcelain | Set-Content "$r/clean-after.log"

