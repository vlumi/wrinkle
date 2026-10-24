// The uploads feature: CreateUpload, and the sweeper of unused uploads.
import type { FeatureFactory } from "../services/features.ts";
import { startSweeper } from "./sweeper.ts";
import { createUpload } from "./uploads.ts";

export const uploads: FeatureFactory = (deps) => ({
  handlers: {
    createUpload: async (caller, req) => ({
      case: "createUpload",
      value: await createUpload(deps, caller.userId, req),
    }),
  },
  start: () => startSweeper(deps),
});
