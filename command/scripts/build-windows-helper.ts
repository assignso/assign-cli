import { mkdirSync } from 'node:fs';
if (process.platform !== 'win32' || !['x64', 'arm64'].includes(process.arch)) {
  throw new Error('Build on native Windows x64 or arm64 in its matching MSVC developer environment');
}
mkdirSync('dist', {recursive: true});
const result = Bun.spawnSync(['cl.exe', '/nologo', '/W4', '/WX', '/O2', '/TC',
  process.arch === 'arm64' ? '/DASSIGN_EXPECT_ARM64' : '/DASSIGN_EXPECT_X64',
  'native/credential-manager.c', '/Fodist/credential-manager.obj',
  '/Fedist/assign-credential-manager.exe', '/link', 'Advapi32.lib'], {stdout:'inherit', stderr:'inherit'});
if (result.exitCode !== 0) throw new Error('Windows credential helper build failed');
