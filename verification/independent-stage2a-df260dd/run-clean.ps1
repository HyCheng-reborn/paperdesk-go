$ErrorActionPreference='Continue'
$env:GOPROXY='off'
$env:GOSUMDB='off'
$root='C:/Zhy/code/go-rag/verification/independent-stage2a-df260dd'
Set-Location 'C:/Zhy/code/go-rag-review-1d3/server'
$commands=@(
 @('ordinary','test','./core/providers/...','-count=1','-timeout=120s','-v'),
 @('race','test','-race','./core/providers/...','-count=1','-timeout=120s'),
 @('race-stress','test','-race','./core/providers/...','-run','^(TestBudget_(ReservationOverflow_(Input|Output|CrossScope)|NegativeUsageConservative|TotalOnlyUsage|SettlementOverflow|NegativeProcessLimit|OverflowSlotRelease|OwnerCancelPropagates|DeadlineNotRefreshedByRetry|TransferKeepsAbsoluteDeadline|ConcurrentReserve_MaxRequests|DualScope_PartialFailure|RetryNoRefresh|TransferToBackground|IdempotentFinalize|ConcurrentFinalize)|TestRegistry_Resolver(CannotMutateProfile|CannotMutateKeyOptions|HoldsCopy))$','-count=10','-timeout=180s','-v'),
 @('full','test','-p','1','./...','-count=1','-timeout=180s'),
 @('build','build','./...'),
 @('vet','vet','./...')
)
foreach($item in $commands){
 $name=$item[0]; $argsList=$item[1..($item.Length-1)]; $log=Join-Path $root ($name+'.log')
 @('SHA: df260ddfb12694e7064d74fb63cc1c228f8ebbfe',('CWD: '+(Get-Location).Path),('CMD: go '+($argsList -join ' ')),('DATE: '+(Get-Date -Format o)),'GOPROXY=off GOSUMDB=off') | Set-Content -LiteralPath $log
 & go @argsList 2>&1 | Out-File -LiteralPath $log -Append -Encoding utf8
 $result=$LASTEXITCODE
 ('EXIT: '+$result) | Add-Content -LiteralPath $log
 Write-Output ($name+' EXIT '+$result)
}


