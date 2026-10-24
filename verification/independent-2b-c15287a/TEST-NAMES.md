# 实际命中名称（去重）

## race-key-stress

=== RUN   TestBailianStreamSession_ConcurrentCloseEOF
=== RUN   TestBailianStreamSession_EOFSettles
=== RUN   TestBailianStreamSession_ExplicitClose
=== RUN   TestBailianStreamSession_NoBudgetFullContent
=== RUN   TestBailianStreamSession_ParentCancelUnderBackpressure
=== RUN   TestBailianStreamSession_ReadError
=== RUN   TestBudget_ConcurrentFinalize
=== RUN   TestBudget_ConcurrentReserve_MaxRequests
=== RUN   TestBudget_EmbeddingSettlesUsage
=== RUN   TestBudget_LocalErrorNoCommit
=== RUN   TestBudget_OutputCapMatches
=== RUN   TestBudget_OutputOptsMatchReservation
=== RUN   TestBudget_OutputOptsRejected
=== RUN   TestBudget_OwnerReuseViaCtx
=== RUN   TestBudget_RealHTTPSend
=== RUN   TestBudget_RedirectCountsAttempts
=== RUN   TestBudget_RetryNoRefresh
=== RUN   TestBudget_StreamFinish
=== RUN   TestBuilder_ConcurrentRegistration
=== RUN   TestBuilder_DuplicateRegistrationRejected
=== RUN   TestBuilder_FrozenAfterRegistryBuild
=== RUN   TestBuilder_NilBuilderRejected
=== RUN   TestBuilder_UnknownProviderRejected
=== RUN   TestEmbedding_ConcurrentUsageIsolated
=== RUN   TestEmbedding_RawIndices_Duplicate
=== RUN   TestEmbedding_RawIndices_Missing
=== RUN   TestEmbedding_RawIndices_OutOfOrder
=== RUN   TestEmbedding_RawIndices_Valid
=== RUN   TestOpenAI_BudgetCoversHTTP
=== RUN   TestOpenAI_StreamBudget
=== RUN   TestOwner_ChatStreamShared
=== RUN   TestOwner_IndexBackgroundTransfer
=== RUN   TestRedirect_AttemptsCounted
=== RUN   TestRedirect_InjectedClientProhibited
=== RUN   TestStream_BlockedSourceClose
=== RUN   TestStream_CancelFinalizes
=== RUN   TestStream_CancelNotEOF
=== RUN   TestStream_ConcurrentClose
=== RUN   TestStream_ConcurrentCloseFinalizes
=== RUN   TestStream_EarlyCloseFinalizes
=== RUN   TestStream_ErrorFinalizes
=== RUN   TestStream_MultiChunkComplete
=== RUN   TestStreamSession_BuildFailReleasesResources
=== RUN   TestStreamSession_CloseInterruptsHTTP
=== RUN   TestStreamSession_CloseInterruptsHTTP_RealHTTP
=== RUN   TestStreamSession_CloseWaitsRecv
=== RUN   TestStreamSession_ConcurrentCloseEOF
=== RUN   TestStreamSession_EarlyClose
=== RUN   TestStreamSession_EOFSettles
=== RUN   TestStreamSession_NoBudgetFullContent
=== RUN   TestStreamSession_ParentCancelBackpressure
=== RUN   TestStreamSession_ParentCancelBackpressure_RealCancel
=== RUN   TestStreamSession_ParentCancelOnly
=== RUN   TestStreamSession_ParentCtxCancel
=== RUN   TestStreamSession_ReadError
=== RUN   TestStreamSession_TempGoroutineTracked
=== RUN   TestStreamSession_WithInnerSessionCascade

## race-chat-key

=== RUN   TestChatStream_SessionBranchesRecycled
=== RUN   TestChatStream_SessionEarlyCloseThenCleanup
=== RUN   TestChatStream_SessionFailCleanup_Backpressure
=== RUN   TestChatStream_SessionFailCleanup_NoPartialSave
=== RUN   TestChatStream_SessionFailNoSave
=== RUN   TestChatStream_SessionRepeatCleanup
=== RUN   TestChatStream_SessionSuccessEOF

## race-managed-review

=== RUN   TestReviewBailianNilBudgetParentCancel
=== RUN   TestReviewManagedChatFailureBackpressure
=== RUN   TestReviewManagedCloseWaitsReceiver
=== RUN   TestReviewManagedNoBudgetABC
=== RUN   TestReviewManagedParentCancelUnderBackpressure
=== RUN   TestReviewManagedRealClose
=== RUN   TestReviewManagedRealClose/bailian
=== RUN   TestReviewManagedRealClose/openai
=== RUN   TestReviewRegistryBailianParentCancelUnderBackpressure
=== RUN   TestReviewSessionEarlyFrontendClose
