# Berkontribusi

Terima kasih sudah tertarik. DMetric adalah aplikasi Go tanpa cgo; UI-nya HTML,
CSS, dan JavaScript biasa tanpa langkah build.

## Menjalankan

```sh
make web      # server saja di http://127.0.0.1:8154
make test     # go test ./...
go vet ./...
```

Build silang tidak membutuhkan toolchain tambahan:
`GOOS=windows GOARCH=amd64 go build ./...`.

## Aturan yang kami pegang

- **Jangan menandai "Normal" bila datanya tidak ada.** Jika sesuatu tidak dapat
  dibaca, tampilkan "tidak diketahui" beserta alasannya.
- **Tes parser memakai fixture.** Untuk kode yang membaca output tool atau OS
  (`ssacli`, `smartctl`, `nvme`, PowerShell), tambahkan fixture. Utamakan
  output perangkat keras nyata, dan tandai di komentar bila fixture itu buatan.
- **Hapus data identitas dari fixture**: nomor seri, WWN/WWID, hostname, IP,
  dan *Unique Identifier*. Ganti dengan nilai buatan yang berbentuk sama.
- **Jujur soal yang belum diuji.** Kode yang hanya terkompilasi (mis. Windows
  tanpa mesin Windows) harus gagal dengan aman dan dicatat di
  [docs/hardware-health.md](docs/hardware-health.md).
- Perubahan yang terlihat pengguna dicatat di
  `internal/server/ui/releases.json`.

## Rilis

Lihat [docs/releasing.md](docs/releasing.md).
