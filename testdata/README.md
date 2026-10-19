# Synthetic RAW fixture

`synthetic.dng` is a generated 128 × 96, 16-bit Bayer gradient in a TIFF/DNG container. It contains no real photograph or personal metadata. Its fictional camera model is `Folderlight synthetic test camera`.

It uses a 2 × 2 RGGB CFA pattern, an identity DNG color matrix, neutral white balance, DNG version 1.4, and uncompressed strips. It is distributed under the repository's MIT license. The media integration test generates its other JPEG, HEIC, and MP4 fixtures at runtime using this project's JPEG generator and the optional Alpine media tools.

`synthetic-preview.dng` contains the same generated Bayer data as a SubIFD, plus a solid-color 128 × 96 JPEG preview in IFD0 using JPEG compression (7), StripOffsets, and StripByteCounts. IFD0 has numeric Orientation 6 (90° clockwise). This generated fixture tests real ExifTool JSON/base64 preview extraction and orientation without relying on a camera photograph; it has the same MIT license.
