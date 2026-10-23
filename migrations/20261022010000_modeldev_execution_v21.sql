-- Coordinated ModelDev/Gov switch only. Stop new submissions and preserve a
-- verified backup before applying. ModelDev's original-UID close/cleanup and
-- all original GPU ledgers must be checked externally; a queued receipt alone
-- is not proof that the original computation stopped.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM public.sys_modeldev_acceptances
    WHERE dispatch_state <> 'ACKED'
       OR close_dispatch_state IN ('QUEUED', 'DISPATCHING', 'UNKNOWN')
  ) THEN
    RAISE EXCEPTION 'ModelDev contract switch requires settled original dispatches';
  END IF;
END $$;

-- The retired pointer is no longer an authorization source. Preserve the
-- original release-binding table, its fields, rows, and permissions for
-- historical review and recovery with the recorded original binary. New
-- admission and query paths never read it and do not accept the retired codec.
-- Settled dispatch receipts do not prove that original computation is CLOSED;
-- original execution fences and cleanup evidence remain independently required.
