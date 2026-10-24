$ErrorActionPreference='Continue'
$env:GOPROXY='off'
$env:GOSUMDB='off'
$root='C:/Zhy/code/go-rag/verification/independent-stage2a-5bf62f0'
Set-Location 'C:/Zhy/code/go-rag-review-1d3/server'
$commands=@(
 @('ordinary','test','./core/providers/...','-count=1','-timeout=120s','-v'),
 @('race','test','-race','./core/providers/...','-count=1','-timeout=120s'),
 @('race-stress','test','-race','./core/providers/...','-run','^TestBudget_(ConcurrentReserve_MaxRequests|DualScope_PartialFailure|RetryNoRefresh|TransferToBackground|IdempotentFinalize|ConcurrentFinalize|SlotRelease_NonStream|SlotRelease_StreamPaths|UnknownUsage)$','-count=10','-timeout=180s','-v'),
 @('full','test','-p','1','./...','-count=1','-timeout=180s'),
 @('build','build','./...'),
 @('vet','vet','./...')
)
foreach($item in $commands){
 $name=$item[0]; $argsList=$item[1..($item.Length-1)]; $log=Join-Path $root ($name+'.log')
 @('SHA: 5bf62f0069a527b4940b8336f3cb953ccb551813',('CWD: '+(Get-Location).Path),('CMD: go '+($argsList -join ' ')),('DATE: '+(Get-Date -Format o)),'GOPROXY=off GOSUMDB=off') | Set-Content -LiteralPath $log
 & go @argsList 2>&1 | Out-File -LiteralPath $log -Append -Encoding utf8
 $result=$LASTEXITCODE
 ('EXIT: '+$result) | Add-Content -LiteralPath $log
 Write-Output ($name+' EXIT '+$result)
}
