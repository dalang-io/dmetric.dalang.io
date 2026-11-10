# Membuat rilis

Rilis dibangun **manual dari mesin lokal**, tanpa CI. Satu perintah membuat
semua berkas, lalu Anda mengunggahnya.

## Langkah

1. **Naikkan versi** (format CalVer `YYYY.M.P`, tag `vYYYY.M.P`):

   ```sh
   scripts/bump-version.sh 2026.12.0
   ```

   Ini menulis versi ke `Makefile`, `mygo.json`, dan `internal/model/model.go`.
   Ketiganya harus sama; skrip rilis menolak jika berbeda.

2. **Tulis catatan rilis** di bagian atas `internal/server/ui/releases.json`:
   `version` (tanpa `v`), `released: true`, `date` (`YYYY-MM-DD`), `title`, dan
   `groups` (`added` / `changed` / `fixed` / `security`). Berkas ini satu-satunya
   sumber: tampil di halaman **Catatan rilis** aplikasi dan menjadi teks rilis
   GitHub.

   ```sh
   make release-check     # memvalidasi entri tanpa membangun
   ```

3. **Bangun**:

   ```sh
   make release
   ```

   Hasil di `dist/release/v<versi>/`:

   | Berkas | Keterangan |
   |---|---|
   | `dmetric-{darwin,linux,windows}-{amd64,arm64}[.exe]` | binary mandiri; **dipakai update dalam aplikasi** |
   | `dmetric-<versi>-{arm64,amd64}.dmg` | disk image macOS (butuh host macOS) |
   | `SHA256SUMS` | checksum semua berkas di atas |
   | `RELEASE_NOTES.md` | dirender dari `releases.json` |

   Skrip juga menjalankan `go vet` dan `go test` (lewati dengan `SKIP_TESTS=1`),
   menolak entri catatan yang masih `released: false`, dan memastikan binary
   mesin ini melaporkan versi yang benar.

4. **Unggah** (skrip hanya mencetak perintahnya; tidak mengunggah apa pun):

   ```sh
   git tag v2026.12.0 && git push origin v2026.12.0
   gh release create v2026.12.0 dist/release/v2026.12.0/dmetric-* \
      dist/release/v2026.12.0/SHA256SUMS \
      --title v2026.12.0 --notes-file dist/release/v2026.12.0/RELEASE_NOTES.md
   ```

Update dalam aplikasi mencari asset bernama persis `dmetric-<os>-<arch>[.exe]`
dan `SHA256SUMS`; jangan mengganti nama berkas itu.

## Catatan

- `.github/workflows/release.yml` (bila masih ada) terpicu oleh tag `v20*` dan
  ikut membangun dan menerbitkan rilis. Untuk rilis yang sepenuhnya manual,
  ubah pemicunya menjadi `workflow_dispatch` atau hapus berkasnya; jika tidak,
  rilis dibuat dua kali.
- DMG memakai *ad-hoc signature*. Untuk distribusi tanpa peringatan Gatekeeper
  perlu Developer ID dan notarization (lihat `docs/updates.md`).
- Build DMG hanya berjalan di macOS; binary mandiri dapat dibangun di mana pun
  karena aplikasi tanpa cgo.
