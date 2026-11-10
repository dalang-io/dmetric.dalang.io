# Pembaruan

DMetric dapat memeriksa dan memasang rilis baru dari GitHub
(`dalang-io/dmetric.dalang.io`, bisa diganti dengan `-update-repo owner/name`
atau `DMETRIC_UPDATE_REPO`). Perilakunya diatur di **Pengaturan → Pembaruan**.

## Mode

| Mode | Perilaku |
|---|---|
| **Manual** (bawaan) | DMetric tidak menghubungi GitHub sendiri. Tombol **Update** di header memeriksa dan memasang saat diklik. |
| **Cek otomatis** | Memeriksa rilis baru sekali tak lama setelah startup (dalam ±1 menit), lalu tiap *N* jam. Hanya memberi tahu di dashboard; tidak memasang. |
| **Pasang otomatis** | Seperti cek otomatis, lalu mengunduh, memverifikasi, memasang, dan me-restart. |

Interval bawaan **24 jam** (1–168 jam). Perubahan mode langsung berlaku tanpa
restart; setelah diaktifkan, pengecekan pertama berjalan dalam ±1 menit.
Status terakhir (waktu cek dan hasilnya) tampil di dialog Update.

## Pengaman pemasangan otomatis

Memasang binary tanpa campur tangan manusia adalah keputusan keamanan, jadi:

- **SHA256 wajib.** Rilis tanpa `SHA256SUMS` yang memuat asset platform ini
  ditolak. (Pemasangan manual lewat tombol masih boleh tanpa checksum, dengan
  peringatan integritas hanya HTTPS.)
- **Tidak mengulang.** Versi yang sudah dicoba dipasang tidak dicoba lagi
  dalam 24 jam, sehingga rilis yang rusak tidak menyebabkan loop
  unduh-restart. Penanda ada di `<data>/updates/auto-attempt.json`.
- **Cadangan.** Binary lama disimpan sebagai `<binary>.prev`. Rollback manual:
  hentikan DMetric, lalu `mv dmetric.prev dmetric`.
- **Hanya binary mandiri.** Tidak berjalan untuk `go run` maupun app macOS
  `.app`; pada kasus itu mode otomatis hanya memeriksa dan dashboard
  menampilkan alasannya.
- **Hanya dari server.** Pengaturan pembaruan (mode dan interval) hanya bisa
  diubah dari **localhost**, sama seperti tombol Update. Client LAN yang
  mencoba mendapat `403`, sehingga tidak bisa mengaktifkan pemasangan otomatis.
- Pemasangan otomatis berarti restart singkat. Riwayat dan alert ada di
  database sehingga tidak hilang.

## macOS

App macOS (`.app` dari DMG) bertanda tangan dan tidak bisa mengganti dirinya.
Gunakan mode **Cek otomatis**, lalu unduh DMG baru dari halaman rilis dan
seret ke Applications. Binary mandiri (`dmetric-darwin-*`) dapat memasang
otomatis seperti Linux.

DMG dibuat dengan *ad-hoc signature*. Di Mac lain, Gatekeeper akan meminta
konfirmasi saat pertama dibuka (klik kanan → Open, atau
`xattr -dr com.apple.quarantine /Applications/DMetric.app`). Signing dengan
Developer ID dan notarization diatur di `mygo.json`
(`macos.signingIdentity`, `macos.notarize`).

## Windows

Binary mandiri memasang lewat helper PowerShell yang menunggu proses lama
berhenti, menyalin cadangan `.prev`, mengganti file, lalu menjalankan ulang
dengan argumen yang sama. Direktori aplikasi harus dapat ditulis oleh akun
yang menjalankan DMetric.

## API

`GET /api/update?refresh=1` mengembalikan info rilis, `install_allowed`
(request dari localhost), dan `auto` (`last_check`, `last_result`,
`last_install`, `can_install`, `block_reason`).
`PUT /api/settings` menerima blok `update`:

```json
{ "update": { "mode": "check", "interval_hours": 24 } }
```

Klien lama yang tidak menyertakan blok `update` tidak mengubah kebijakan.
