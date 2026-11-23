/**
 * Minimal ISO/IEC 18004 QR Code encoder (zero dependencies).
 * Byte mode (UTF-8), error correction level M, versions 1–10 (up to 213 bytes),
 * GF(256) Reed–Solomon, best of 8 masks by penalty score.
 */

// Per version 1..10 at EC level M: EC codewords per block, and [blockCount, dataCodewordsPerBlock] groups.
const EC_PER_BLOCK = [10, 16, 26, 18, 24, 16, 18, 22, 22, 26]
const BLOCK_GROUPS: [number, number][][] = [
  [[1, 16]],
  [[1, 28]],
  [[1, 44]],
  [[2, 32]],
  [[2, 43]],
  [[4, 27]],
  [[4, 31]],
  [[2, 38], [2, 39]],
  [[3, 36], [2, 37]],
  [[4, 43], [1, 44]],
]
const ALIGNMENT: number[][] = [
  [],
  [6, 18],
  [6, 22],
  [6, 26],
  [6, 30],
  [6, 34],
  [6, 22, 38],
  [6, 24, 42],
  [6, 26, 46],
  [6, 28, 50],
]
const MAX_VERSION = 10
const EC_FORMAT_BITS_M = 0b00

const dataCapacity = (ver: number) => BLOCK_GROUPS[ver - 1].reduce((n, [count, len]) => n + count * len, 0)

// ---------------------------------------------------------------------------
// GF(256) arithmetic, primitive polynomial x^8 + x^4 + x^3 + x^2 + 1 (0x11D)

function gfMul(x: number, y: number): number {
  let z = 0
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d)
    z ^= ((y >>> i) & 1) * x
  }
  return z & 0xff
}

/** Generator polynomial coefficients (highest-degree term omitted) for roots α^0..α^(degree-1). */
export function rsDivisor(degree: number): number[] {
  const result = new Array<number>(degree).fill(0)
  result[degree - 1] = 1
  let root = 1
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < result.length; j++) {
      result[j] = gfMul(result[j], root)
      if (j + 1 < result.length) result[j] ^= result[j + 1]
    }
    root = gfMul(root, 0x02)
  }
  return result
}

/** Reed–Solomon EC codewords for one block. */
export function rsRemainder(data: ArrayLike<number>, divisor: number[]): number[] {
  const result = new Array<number>(divisor.length).fill(0)
  for (let k = 0; k < data.length; k++) {
    const factor = data[k] ^ (result.shift() as number)
    result.push(0)
    for (let i = 0; i < divisor.length; i++) result[i] ^= gfMul(divisor[i], factor)
  }
  return result
}

// ---------------------------------------------------------------------------
// Data encoding

function encodeCodewords(bytes: Uint8Array): { version: number; codewords: number[] } {
  let version = 0
  for (let v = 1; v <= MAX_VERSION; v++) {
    const ccBits = v < 10 ? 8 : 16
    if (4 + ccBits + bytes.length * 8 <= dataCapacity(v) * 8) {
      version = v
      break
    }
  }
  if (!version) throw new RangeError(`QR payload too long (${bytes.length} bytes, max ${dataCapacity(MAX_VERSION) - 3})`)

  const capBits = dataCapacity(version) * 8
  const bits: number[] = []
  const push = (val: number, len: number) => {
    for (let i = len - 1; i >= 0; i--) bits.push((val >>> i) & 1)
  }
  push(0b0100, 4) // byte mode
  push(bytes.length, version < 10 ? 8 : 16)
  bytes.forEach((b) => push(b, 8))
  push(0, Math.min(4, capBits - bits.length)) // terminator
  push(0, (8 - (bits.length % 8)) % 8)
  for (let pad = 0xec; bits.length < capBits; pad ^= 0xec ^ 0x11) push(pad, 8)

  const data: number[] = []
  for (let i = 0; i < bits.length; i += 8) {
    let b = 0
    for (let j = 0; j < 8; j++) b = (b << 1) | bits[i + j]
    data.push(b)
  }

  // Split into blocks, add EC, interleave.
  const divisor = rsDivisor(EC_PER_BLOCK[version - 1])
  const dataBlocks: number[][] = []
  const ecBlocks: number[][] = []
  let k = 0
  for (const [count, len] of BLOCK_GROUPS[version - 1]) {
    for (let c = 0; c < count; c++) {
      const block = data.slice(k, k + len)
      k += len
      dataBlocks.push(block)
      ecBlocks.push(rsRemainder(block, divisor))
    }
  }
  const codewords: number[] = []
  const maxLen = Math.max(...dataBlocks.map((b) => b.length))
  for (let i = 0; i < maxLen; i++) for (const b of dataBlocks) if (i < b.length) codewords.push(b[i])
  for (let i = 0; i < divisor.length; i++) for (const b of ecBlocks) codewords.push(b[i])
  return { version, codewords }
}

// ---------------------------------------------------------------------------
// Matrix construction

class Matrix {
  readonly version: number
  readonly size: number
  readonly dark: boolean[][]
  readonly fn: boolean[][]

  constructor(version: number) {
    this.version = version
    this.size = version * 4 + 17
    this.dark = Array.from({ length: this.size }, () => new Array<boolean>(this.size).fill(false))
    this.fn = Array.from({ length: this.size }, () => new Array<boolean>(this.size).fill(false))
  }

  setFn(x: number, y: number, dark: boolean) {
    this.dark[y][x] = dark
    this.fn[y][x] = true
  }

  drawFunctionPatterns() {
    const { size } = this
    for (let i = 0; i < size; i++) {
      this.setFn(6, i, i % 2 === 0)
      this.setFn(i, 6, i % 2 === 0)
    }
    this.drawFinder(3, 3)
    this.drawFinder(size - 4, 3)
    this.drawFinder(3, size - 4)
    const pos = ALIGNMENT[this.version - 1]
    const n = pos.length
    for (let i = 0; i < n; i++) {
      for (let j = 0; j < n; j++) {
        if ((i === 0 && j === 0) || (i === 0 && j === n - 1) || (i === n - 1 && j === 0)) continue
        this.drawAlignment(pos[i], pos[j])
      }
    }
    this.drawFormat(0) // reserve; redrawn after mask selection
    this.drawVersion()
  }

  drawFinder(cx: number, cy: number) {
    for (let dy = -4; dy <= 4; dy++) {
      for (let dx = -4; dx <= 4; dx++) {
        const x = cx + dx
        const y = cy + dy
        if (x < 0 || y < 0 || x >= this.size || y >= this.size) continue
        const d = Math.max(Math.abs(dx), Math.abs(dy))
        this.setFn(x, y, d !== 2 && d !== 4)
      }
    }
  }

  drawAlignment(cx: number, cy: number) {
    for (let dy = -2; dy <= 2; dy++)
      for (let dx = -2; dx <= 2; dx++) this.setFn(cx + dx, cy + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1)
  }

  drawFormat(mask: number) {
    const data = (EC_FORMAT_BITS_M << 3) | mask
    let rem = data
    for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537)
    const bits = ((data << 10) | rem) ^ 0x5412
    const bit = (i: number) => ((bits >>> i) & 1) !== 0
    const { size } = this
    for (let i = 0; i <= 5; i++) this.setFn(8, i, bit(i))
    this.setFn(8, 7, bit(6))
    this.setFn(8, 8, bit(7))
    this.setFn(7, 8, bit(8))
    for (let i = 9; i < 15; i++) this.setFn(14 - i, 8, bit(i))
    for (let i = 0; i < 8; i++) this.setFn(size - 1 - i, 8, bit(i))
    for (let i = 8; i < 15; i++) this.setFn(8, size - 15 + i, bit(i))
    this.setFn(8, size - 8, true) // dark module
  }

  drawVersion() {
    if (this.version < 7) return
    let rem = this.version
    for (let i = 0; i < 12; i++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25)
    const bits = (this.version << 12) | rem
    for (let i = 0; i < 18; i++) {
      const dark = ((bits >>> i) & 1) !== 0
      const a = this.size - 11 + (i % 3)
      const b = Math.floor(i / 3)
      this.setFn(a, b, dark)
      this.setFn(b, a, dark)
    }
  }

  placeCodewords(codewords: number[]) {
    const { size } = this
    const total = codewords.length * 8
    let i = 0
    for (let right = size - 1; right >= 1; right -= 2) {
      if (right === 6) right = 5
      const upward = ((right + 1) & 2) === 0
      for (let vert = 0; vert < size; vert++) {
        const y = upward ? size - 1 - vert : vert
        for (let j = 0; j < 2; j++) {
          const x = right - j
          if (this.fn[y][x]) continue
          // Remainder bits (past the last codeword) stay light.
          this.dark[y][x] = i < total && ((codewords[i >>> 3] >>> (7 - (i & 7))) & 1) !== 0
          i++
        }
      }
    }
  }

  applyMask(mask: number) {
    const { size } = this
    for (let y = 0; y < size; y++) {
      for (let x = 0; x < size; x++) {
        if (this.fn[y][x]) continue
        let invert: boolean
        switch (mask) {
          case 0: invert = (x + y) % 2 === 0; break
          case 1: invert = y % 2 === 0; break
          case 2: invert = x % 3 === 0; break
          case 3: invert = (x + y) % 3 === 0; break
          case 4: invert = (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0; break
          case 5: invert = ((x * y) % 2) + ((x * y) % 3) === 0; break
          case 6: invert = (((x * y) % 2) + ((x * y) % 3)) % 2 === 0; break
          default: invert = (((x + y) % 2) + ((x * y) % 3)) % 2 === 0; break
        }
        if (invert) this.dark[y][x] = !this.dark[y][x]
      }
    }
  }

  penalty(): number {
    const { size, dark } = this
    let score = 0
    const at = (x: number, y: number, horizontal: boolean) => (horizontal ? dark[y][x] : dark[x][y])

    for (const horizontal of [true, false]) {
      for (let a = 0; a < size; a++) {
        // N1: runs of 5+ same-coloured modules
        let run = 1
        for (let b = 1; b <= size; b++) {
          if (b < size && at(b, a, horizontal) === at(b - 1, a, horizontal)) {
            run++
          } else {
            if (run >= 5) score += 3 + (run - 5)
            run = 1
          }
        }
        // N3: 1:1:3:1:1 finder-like pattern with 4 light modules on one side (out-of-bounds counts as light)
        const get = (b: number) => (b < 0 || b >= size ? false : at(b, a, horizontal))
        for (let b = -4; b < size; b++) {
          const core =
            get(b) && !get(b + 1) && get(b + 2) && get(b + 3) && get(b + 4) && !get(b + 5) && get(b + 6)
          if (!core) continue
          const lightBefore = !get(b - 1) && !get(b - 2) && !get(b - 3) && !get(b - 4)
          const lightAfter = !get(b + 7) && !get(b + 8) && !get(b + 9) && !get(b + 10)
          if (lightBefore || lightAfter) score += 40
        }
      }
    }

    // N2: 2×2 blocks
    for (let y = 0; y < size - 1; y++) {
      for (let x = 0; x < size - 1; x++) {
        const c = dark[y][x]
        if (c === dark[y][x + 1] && c === dark[y + 1][x] && c === dark[y + 1][x + 1]) score += 3
      }
    }

    // N4: dark/light balance
    let darkCount = 0
    for (const row of dark) for (const c of row) if (c) darkCount++
    const total = size * size
    const k = Math.ceil(Math.abs(darkCount * 20 - total * 10) / total) - 1
    score += Math.max(0, k) * 10
    return score
  }
}

function build(version: number, codewords: number[], mask: number): Matrix {
  const m = new Matrix(version)
  m.drawFunctionPatterns()
  m.placeCodewords(codewords)
  m.applyMask(mask)
  m.drawFormat(mask)
  return m
}

/**
 * Encode text as a QR Code module matrix (`true` = dark), indexed `[row][column]`, without quiet zone.
 * `forceMask` (0–7) skips automatic mask selection.
 */
export function encodeQR(text: string, forceMask?: number): boolean[][] {
  const { version, codewords } = encodeCodewords(new TextEncoder().encode(text))
  if (forceMask !== undefined) {
    if (!Number.isInteger(forceMask) || forceMask < 0 || forceMask > 7) throw new RangeError('mask must be 0..7')
    return build(version, codewords, forceMask).dark
  }
  let best: Matrix | null = null
  let bestScore = Infinity
  for (let mask = 0; mask < 8; mask++) {
    const m = build(version, codewords, mask)
    const s = m.penalty()
    if (s < bestScore) {
      best = m
      bestScore = s
    }
  }
  return (best as Matrix).dark
}

export interface QRSvgOptions {
  /** Pixels per module for the width/height attributes (default 4). */
  scale?: number
  /** Quiet-zone width in modules (default 4). */
  margin?: number
}

/**
 * Render as a self-contained SVG string: fixed white background and black modules
 * so it stays scannable in dark mode.
 */
export function qrToSVG(text: string, { scale = 4, margin = 4 }: QRSvgOptions = {}): string {
  const matrix = encodeQR(text)
  const n = matrix.length + margin * 2
  let d = ''
  matrix.forEach((row, y) => {
    let x = 0
    while (x < row.length) {
      if (!row[x]) {
        x++
        continue
      }
      const start = x
      while (x < row.length && row[x]) x++
      d += `M${start + margin} ${y + margin}h${x - start}v1h-${x - start}z`
    }
  })
  const px = n * scale
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${n} ${n}" width="${px}" height="${px}" shape-rendering="crispEdges">` +
    `<rect width="${n}" height="${n}" fill="#ffffff"/><path d="${d}" fill="#000000"/></svg>`
  )
}

/** `data:` URL form of {@link qrToSVG}, usable as an `<img src>` under a `img-src data:` CSP. */
export function qrToDataURL(text: string, opts?: QRSvgOptions): string {
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(qrToSVG(text, opts))}`
}
