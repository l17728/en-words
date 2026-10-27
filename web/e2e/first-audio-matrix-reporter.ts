import type { FullResult, Reporter, TestResult } from '@playwright/test/reporter'
import { writeFile } from 'node:fs/promises'

type Options = { outputFile: string }

// Persist aggregate counts only. Do not serialize test titles, assertion values,
// stdout, or captions into the private matrix artifact directory.
export default class FirstAudioMatrixReporter implements Reporter {
  private readonly outputFile: string
  private expected = 0
  private unexpected = 0
  private flaky = 0
  private skipped = 0

  constructor(options: Options) {
    this.outputFile = options.outputFile
  }

  onTestEnd(_test: unknown, result: TestResult): void {
    if (result.status === 'skipped') {
      this.skipped++
    } else if (result.status === 'passed' && result.retry === 0) {
      this.expected++
    } else if (result.status === 'passed') {
      this.flaky++
    } else {
      this.unexpected++
    }
  }

  async onEnd(result: FullResult): Promise<void> {
    const unexpected = this.unexpected + (result.status === 'passed' ? 0 : 1)
    await writeFile(this.outputFile, JSON.stringify({
      stats: {
        expected: this.expected,
        unexpected,
        flaky: this.flaky,
        skipped: this.skipped,
      },
    }), { encoding: 'utf8', flag: 'wx', mode: 0o600 })
  }
}
