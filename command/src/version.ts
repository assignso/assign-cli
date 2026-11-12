export interface BuildIdentity {
  version: string;
  commit: string;
  date: string;
}

declare const ASSIGN_BUILD_IDENTITY: BuildIdentity | undefined;

export const developmentBuild: BuildIdentity = typeof ASSIGN_BUILD_IDENTITY !== "undefined"
  ? ASSIGN_BUILD_IDENTITY
  : { version: "dev", commit: "none", date: "unknown" };

export function formatVersion(build = developmentBuild): string {
  return `assign ${build.version}\ncommit ${build.commit}\nbuilt ${build.date}\n`;
}
