// The uploads feature: CreateUpload, and the sweeper of unused uploads.
import type { FeatureFactory } from "../services/features.ts";
import { startSweeper, type SweepOptions } from "./sweeper.ts";
import { createUpload } from "./uploads.ts";

export const uploadsFeature =
  (sweeping: SweepOptions): FeatureFactory =>
  (deps) => ({
    handlers: {
      createUpload: async (caller, req) => ({
        case: "createUpload",
        value: await createUpload(deps, caller.userId, req),
      }),
    },
    start: () => startSweeper(deps, sweeping),
  });
