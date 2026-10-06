// Tests for scheduled backup definitions (launchd, systemd, cron and Windows Task Scheduler) and
// the Scheduler's install and remove steps. Every command goes to a stub runner, so no real
// scheduler or lingering setting is touched.

import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {
  Scheduler,
  hour,
  launchdPlist,
  systemdUnits,
  cronLine,
  stripCron,
  taskXML,
  taskCommand,
  utf16File,
  windowsQuote,
  lingerMarker,
  onCalendar,
  cronSpec,
} from '../../src/platform/schedule.js';
import type { Runner } from '../../src/platform/command.js';

const job = { binary: '/usr/local/bin/frost', every: 6 * hour, logFile: '/home/me/.cache/frost/frost.log' };

// Paths with quotes, ampersands and angle brackets must survive each format's escaping. Cron and
// launchd jobs run at minute 17, and daily and weekly ones at 03:17.
test('scheduler definitions preserve interval, quoting, and logging', () => {
  const j = { ...job, configDir: "/it's here", cacheDir: '/cache & <files>' };
  assert.match(launchdPlist(j), /\/cache &amp; &lt;files&gt;/);
  assert.doesNotMatch(launchdPlist(j), /StartInterval/);
  const { service, timer } = systemdUnits(j);
  assert.match(service, /ExecStart="\/usr\/local\/bin\/frost" backup --scheduled/);
  assert.match(timer, /OnCalendar=\*-\*-\* 00\/6:00:00\nPersistent=true/);
  assert.equal(
    cronLine({ ...job, configDir: "/it's here" }),
    "17 */6 * * * '/usr/local/bin/frost' backup --scheduled --config-dir '/it'\\''s here' >> '/home/me/.cache/frost/frost.log' 2>&1 # frost-backup",
  );

  // Only frost's own line is removed, never a line that merely mentions the marker.
  assert.equal(stripCron('0 1 * * * other\n' + cronLine(job) + '\n'), '0 1 * * * other\n');
  assert.equal(stripCron("* * * * * echo '# frost-backup'\n"), "* * * * * echo '# frost-backup'\n");
  assert.equal(stripCron(cronLine(job) + '\n'), '');

  for (const [every, calendar, cron] of [
    [hour, 'hourly', '17 * * * *'],
    [24 * hour, 'daily', '17 3 * * *'],
    [168 * hour, 'weekly', '17 3 * * 0'],
  ] as const) {
    assert.equal(onCalendar(every), calendar);
    assert.equal(cronSpec(every), cron);
  }

  // launchd gets one calendar entry per run time, in the same order as cron's fields.
  const launchdTimes = (every: number) =>
    [
      ...launchdPlist({ ...job, every }).matchAll(
        /<dict>\n((?:\t+<key>\w+<\/key>\n\t+<integer>\d+<\/integer>\n)+)\t+<\/dict>/g,
      ),
    ].map(m =>
      [...m[1].matchAll(/<key>(\w+)<\/key>\n\t+<integer>(\d+)<\/integer>/g)].map(f => f[1] + '=' + f[2]).join(' '),
    );
  assert.deepEqual(launchdTimes(hour), ['Minute=17']);
  assert.deepEqual(launchdTimes(6 * hour), [
    'Hour=0 Minute=17',
    'Hour=6 Minute=17',
    'Hour=12 Minute=17',
    'Hour=18 Minute=17',
  ]);
  assert.deepEqual(launchdTimes(8 * hour), ['Hour=0 Minute=17', 'Hour=8 Minute=17', 'Hour=16 Minute=17']);
  assert.deepEqual(launchdTimes(24 * hour), ['Hour=3 Minute=17']);
  assert.deepEqual(launchdTimes(168 * hour), ['Weekday=0 Hour=3 Minute=17']);
});
// Installed frost runs as the bundled runtime plus a launch script, which must stay two arguments.
test('signed runtime and script remain separate scheduler arguments', () => {
  const j = { ...job, binary: '/app/runtime/bin/node', script: '/app/launch.mjs' };
  assert.match(launchdPlist(j), /<string>\/app\/runtime\/bin\/node<\/string>\n\t\t<string>\/app\/launch.mjs<\/string>/);
  assert.match(systemdUnits(j).service, /ExecStart="\/app\/runtime\/bin\/node" "\/app\/launch.mjs" backup --scheduled/);
  assert.match(cronLine(j), /'\/app\/runtime\/bin\/node' '\/app\/launch.mjs' backup --scheduled/);
  assert.match(taskCommand(j).args, /^"\/app\/launch.mjs" backup --scheduled/);
});
// systemd expands % specifiers and $ variables, and cron treats % as a newline, so each must be
// escaped. A value that looks like a flag or a shell command stays one literal argument.
test('systemd and cron escape substitutions in paths', () => {
  const j = { ...job, binary: '/path/100%/$HOME/frost', configDir: '/config/%h/$USER' };
  assert.match(systemdUnits(j).service, /100%%\/\$\$HOME/);
  assert.match(systemdUnits(j).service, /%%h\/\$\$USER/);
  assert.match(cronLine(j), /100\\%/);

  const literal = { ...job, script: 'backup', configDir: '--name; $(touch file)' };
  assert.match(systemdUnits(literal).service, /"backup" backup --scheduled --config-dir "--name; \$\$\(touch file\)"/);
  assert.match(cronLine(literal), /'backup' backup --scheduled --config-dir '--name; \$\(touch file\)'/);
  assert.match(taskCommand(literal).args, /^"backup" backup --scheduled --config-dir "--name; \$\(touch file\)"/);
});
// Windows command lines double backslashes only before a quote, so a trailing backslash in a
// quoted path becomes two. The task runs as the signed-in user with least privilege.
test('Windows task preserves registration, least privilege, schedule, and paths', () => {
  const now = new Date(2026, 9, 4, 11, 18, 40);
  const j = {
    binary: 'C:\\program files\\node.exe',
    script: 'C:\\frost\\launch.mjs',
    configDir: 'C:\\backup config\\',
    cacheDir: 'C:\\cache & 100% !\\',
    logFile: 'C:\\log\\frost.log',
    every: hour,
  };
  const { command, args } = taskCommand(j);
  assert.equal(command, '"C:\\program files\\node.exe"');
  assert.match(args, /--config-dir "C:\\backup config\\\\"/);
  assert.match(args, /--cache-dir "C:\\cache & 100% !\\\\"/);
  assert.match(args, /--log-file "C:\\log\\frost.log"/);

  // Tasks shorter than a day start at the current minute. Daily and weekly ones start at 03:17,
  // and weekly means Sunday.
  const xml = taskXML(j, now);
  assert.match(xml, /<StartBoundary>2026-10-04T11:18:00<\/StartBoundary>/);
  assert.match(xml, /<Interval>PT1H<\/Interval>/);
  assert.match(xml, /<Author>frost<\/Author>/);
  assert.match(xml, /<LogonType>InteractiveToken<\/LogonType>/);
  assert.match(xml, /<RunLevel>LeastPrivilege<\/RunLevel>/);
  assert.match(xml, /<MultipleInstancesPolicy>IgnoreNew<\/MultipleInstancesPolicy>/);
  assert.match(xml, /&amp;/);
  assert.match(taskXML({ ...j, every: 24 * hour }, now), /2026-10-04T03:17:00/);
  assert.match(taskXML({ ...j, every: 168 * hour }, now), /<Sunday\/>/);

  // Task XML is written as UTF-16LE with a byte order mark (FF FE).
  assert.equal(utf16File('<a>é😀</a>').subarray(2).toString('utf16le'), '<a>é😀</a>');
  assert.deepEqual([...utf16File('a').subarray(0, 2)], [255, 254]);
  assert.equal(windowsQuote('a\\"b\\'), '"a\\\\\\"b\\\\"');
});
// A Linux install with a stub runner and a private home. Lingering reports "no", so frost enables
// it, marks the timer as the owner of that change, and disables it again on removal.
test('scheduler tests never run the real scheduler', async t => {
  const home = await mkdtemp(path.join(os.tmpdir(), 'frost-schedule-test-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const calls: [string, string[], string | Buffer | undefined][] = [];
  const runner: Runner = async (cmd, args, input) => {
    calls.push([cmd, args, input]);
    return { code: 0, stdout: args.includes('--property=Linger') ? 'no\n' : '', stderr: '' };
  };
  const scheduler = new Scheduler({ platform: 'linux', home, env: {}, uid: 123, runner });

  await scheduler.install(job);
  assert.ok(calls.some(([cmd, args]) => cmd === 'loginctl' && args.includes('enable-linger')));
  assert.match(
    await readFile(path.join(home, '.config', 'systemd', 'user', 'frost-backup.timer'), 'utf8'),
    new RegExp(lingerMarker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')),
  );
  await scheduler.remove();
  assert.ok(calls.some(([cmd, args]) => cmd === 'loginctl' && args.includes('disable-linger')));

  await assert.rejects(scheduler.install({ ...job, binary: 'bad\npath' }), /line breaks/);
  await assert.rejects(scheduler.install({ ...job, every: 1 }), /at least an hour/);
});

// Switching frost off under Login Items unloads the launchd job and leaves its plist behind, so an
// installed job needs both the plist and a loaded service.
test('a launchd job counts as installed only while launchd has it loaded', async t => {
  const home = await mkdtemp(path.join(os.tmpdir(), 'frost-launchd-test-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  let loaded = true;
  const calls: string[][] = [];
  const runner: Runner = async (cmd, args) => {
    calls.push([cmd, ...args]);
    return { code: cmd === 'launchctl' && args[0] === 'print' && !loaded ? 113 : 0, stdout: '', stderr: '' };
  };
  const scheduler = new Scheduler({ platform: 'darwin', home, uid: 501, runner });
  assert.equal(await scheduler.installed(), false);

  await scheduler.install(job);
  const plist = path.join(home, 'Library', 'LaunchAgents', 'io.github.rhymeswithlimo.frost.plist');
  assert.match(await readFile(plist, 'utf8'), /StartCalendarInterval/);
  assert.deepEqual(calls.at(-1), ['launchctl', 'bootstrap', 'gui/501', plist]);
  assert.equal(await scheduler.installed(), true);
  assert.deepEqual(calls.at(-1), ['launchctl', 'print', 'gui/501/io.github.rhymeswithlimo.frost']);

  loaded = false;
  assert.equal(await scheduler.installed(), false);
  await scheduler.remove();
  await assert.rejects(readFile(plist), { code: 'ENOENT' });
});
// The stub inspects the XML file handed to `schtasks /Create`, which must be deleted afterwards.
test('Windows task is UTF-16 and gets removed after mocked registration', async t => {
  const cacheDir = await mkdtemp(path.join(os.tmpdir(), 'frost-task-test-'));
  t.after(() => rm(cacheDir, { recursive: true, force: true }));
  let xmlFile = '';
  const runner: Runner = async (cmd, args) => {
    if (args.includes('/Create')) {
      xmlFile = args.at(-1)!;
      const b = await readFile(xmlFile);
      assert.equal(b.readUInt16LE(0), 0xfeff);
      assert.match(b.subarray(2).toString('utf16le'), /frost backup/);
    }
    return { code: 0, stdout: '', stderr: '' };
  };
  const scheduler = new Scheduler({ platform: 'win32', runner });
  await scheduler.install({ ...job, cacheDir });
  await assert.rejects(readFile(xmlFile), { code: 'ENOENT' });
});

// schtasks messages are translated, so removal asks whether the task still exists instead of
// reading the error text.
test('Windows task removal checks for the task instead of parsing messages', async () => {
  for (const [stillThere, fails] of [
    [false, false],
    [true, true],
  ]) {
    const runner: Runner = async (_cmd, args) => {
      if (args.includes('/Delete')) return { code: 1, stdout: '', stderr: 'FEHLER: Die Aufgabe wurde nicht gefunden.' };
      if (args.includes('/Query')) return { code: stillThere ? 0 : 1, stdout: '', stderr: '' };
      return { code: 0, stdout: '', stderr: '' };
    };
    const removal = new Scheduler({ platform: 'win32', runner }).remove();
    if (fails) await assert.rejects(removal, /schtasks: FEHLER/);
    else await removal;
  }
});
