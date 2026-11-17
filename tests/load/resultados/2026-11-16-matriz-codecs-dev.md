# Matriz de compatibilidade — PC de desenvolvimento

> **Escopo:** validação de software apenas. O teste foi feito num PC de desenvolvimento com Windows 11, não no servidor de laboratório, e não mede capacidade nem testa players reais.

- **Data:** 2026-11-16
- **Ambiente:**
  - Windows 11 Pro;
  - FFmpeg 9.0.2 (build full da Gyan);
  - encoders detectados: cpu e qsv (Intel Quick Sync).
- **Teste:** `go test ./tests/integration/ -run TestCompatMatrix -v`
- **Fonte:** `testsrc2` 640×360, 25 fps, mais um tom de 440 Hz, enviada por UDP/MPEG-TS.
- **Canal:** modo automático, perfil padrão `sd` em CPU.

| Entrada | Contêiner | Vídeo | Áudio | Decisão do pipeline | Saída HLS | Resultado |
| --- | --- | --- | --- | --- | --- | --- |
| UDP | MPEG-TS | H.264 | AAC | passthrough | H.264 + AAC | ✅ |
| UDP | MPEG-TS | H.264 | MP2 | áudio convertido | H.264 + AAC | ✅ |
| UDP | MPEG-TS | H.264 | AC-3 | áudio convertido | H.264 + AAC | ✅ |
| UDP | MPEG-TS | MPEG-2 | MP2 | transcodificação | H.264 + AAC | ✅ |
| UDP | MPEG-TS | HEVC | AAC | transcodificação | H.264 + AAC | ✅ (HEVC repassado só com a opção ligada) |

## Observações

- **ABR:** também foi testado com 2 qualidades em CPU (`TestFFmpegABR`). O ffprobe confirmou a resolução de cada qualidade.
- **Quick Sync com sinal real:** num canal ABR alimentado pelo relay do Apple bipbop, o `h264_qsv` falhou com "device failed (-17)" depois de cerca de 50 s, de forma repetida. Com a quarentena de encoder, o canal voltou sozinho em CPU.
  - Em testes curtos, de 5 a 10 s, o QSV funcionou, então é preciso repetir no hardware de produção, com o driver atual, por horas.
- **Ainda não validado:** desentrelaçamento de MPEG-2 entrelaçado real, AC-3 5.1, HEVC 10 bits e players reais.
